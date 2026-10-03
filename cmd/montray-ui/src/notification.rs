use anyhow::{Context, Result};
use notify_rust::Notification;
use std::sync::mpsc::{self, Receiver, SyncSender, TrySendError};
use std::thread::{self, JoinHandle};
use std::time::Duration;

/// Maximum number of notifications waiting behind the one currently being shown.
const NOTIFICATION_QUEUE_CAPACITY: usize = 32;
/// Maximum shutdown delay when the platform notification service is stuck.
const NOTIFICATION_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(2);

/// Injectable boundary around the desktop notification service.
pub trait NotificationSink: Send + 'static {
    fn push(&self, title: &str, body: &str) -> Result<()>;
}

/// Production sink backed by the platform notification daemon.
#[derive(Clone, Copy, Debug, Default)]
pub struct DesktopNotificationSink;

impl NotificationSink for DesktopNotificationSink {
    fn push(&self, title: &str, body: &str) -> Result<()> {
        desktop_notification(title, body)
            .show()
            .context("desktop notification delivery failed")?;
        Ok(())
    }
}

/// One notification owned by the delivery worker.
struct QueuedNotification {
    title: String,
    body: String,
}

/// Result of a nonblocking attempt to enqueue a notification.
#[derive(Debug, Eq, PartialEq)]
enum EnqueueOutcome {
    Queued,
    Dropped,
    Unavailable,
}

/// Serializes desktop notifications without blocking the async event pump.
///
/// The bounded queue deliberately drops newly generated notifications when full:
/// stale incident transitions are less useful than keeping current network and UI
/// state responsive. Accepted notifications are delivered in reducer order.
pub struct NotificationDispatcher {
    /// Bounded producer endpoint; dropping it tells the worker to drain and stop.
    sender: Option<SyncSender<QueuedNotification>>,
    /// Completion signal used to put a deadline on shutdown.
    stopped: Receiver<()>,
    /// Owned while joinable and deliberately discarded if shutdown times out.
    worker: Option<JoinHandle<()>>,
}

impl NotificationDispatcher {
    /// Starts the production desktop-notification worker.
    pub fn start() -> Result<Self> {
        Self::start_with_sink(DesktopNotificationSink, NOTIFICATION_QUEUE_CAPACITY)
    }

    /// Queues a notification, or drops it immediately if all queue slots are busy.
    pub fn enqueue(&mut self, title: String, body: String) {
        let _ = self.enqueue_inner(title, body);
    }

    /// Drains accepted notifications and waits briefly for the worker to finish.
    ///
    /// A notification daemon call may block inside platform code. In that case the
    /// worker thread is detached after the timeout so application shutdown can
    /// still complete; process exit then terminates the detached thread.
    pub fn shutdown(mut self) {
        let _ = self.shutdown_with_timeout(NOTIFICATION_SHUTDOWN_TIMEOUT);
    }

    /// Starts a dispatcher with an injectable sink and queue size for tests.
    fn start_with_sink<S>(sink: S, capacity: usize) -> Result<Self>
    where
        S: NotificationSink,
    {
        let (sender, receiver) = mpsc::sync_channel::<QueuedNotification>(capacity);
        let (stopped_sender, stopped) = mpsc::channel();
        let worker = thread::Builder::new()
            .name("montray-ui-notifications".into())
            .spawn(move || {
                while let Ok(notification) = receiver.recv() {
                    if let Err(error) = sink.push(&notification.title, &notification.body) {
                        log::error!("{error:#}");
                    }
                }
                let _ = stopped_sender.send(());
            })
            .context("failed to start desktop notification worker")?;

        Ok(Self {
            sender: Some(sender),
            stopped,
            worker: Some(worker),
        })
    }

    /// Implements enqueueing and exposes the state transition to unit tests.
    fn enqueue_inner(&mut self, title: String, body: String) -> EnqueueOutcome {
        let notification = QueuedNotification { title, body };
        let result = match self.sender.as_ref() {
            Some(sender) => sender.try_send(notification),
            None => return EnqueueOutcome::Unavailable,
        };

        match result {
            Ok(()) => EnqueueOutcome::Queued,
            Err(TrySendError::Full(_)) => {
                log::warn!("desktop notification queue is full; dropping notification");
                EnqueueOutcome::Dropped
            }
            Err(TrySendError::Disconnected(_)) => {
                self.sender.take();
                log::error!("desktop notification worker stopped unexpectedly");
                EnqueueOutcome::Unavailable
            }
        }
    }

    /// Closes the queue and joins the worker unless delivery exceeds `timeout`.
    fn shutdown_with_timeout(&mut self, timeout: Duration) -> ShutdownOutcome {
        self.sender.take();

        match self.stopped.recv_timeout(timeout) {
            Ok(()) | Err(mpsc::RecvTimeoutError::Disconnected) => {
                if self
                    .worker
                    .take()
                    .is_some_and(|worker| worker.join().is_err())
                {
                    log::error!("desktop notification worker panicked");
                } else {
                    log::info!("desktop notification worker stopped");
                }
                ShutdownOutcome::Stopped
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {
                self.worker.take();
                log::warn!(
                    "desktop notification worker did not stop within {}s; detaching it",
                    timeout.as_secs_f64()
                );
                ShutdownOutcome::Detached
            }
        }
    }
}

impl Drop for NotificationDispatcher {
    fn drop(&mut self) {
        // Closing the last sender lets the worker finish even if an early return
        // bypasses the explicit, bounded shutdown path.
        self.sender.take();
    }
}

/// Whether an explicit shutdown joined or detached the worker.
#[derive(Debug, Eq, PartialEq)]
enum ShutdownOutcome {
    Stopped,
    Detached,
}

/// Constructs notification metadata separately so it can be tested without delivery.
fn desktop_notification(title: &str, body: &str) -> Notification {
    let mut notification = Notification::new();
    notification.appname("Montray").summary(title).body(body);
    notification
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::Instant;

    /// Sink that records every delivered notification.
    struct RecordingSink(Arc<Mutex<Vec<(String, String)>>>);

    impl NotificationSink for RecordingSink {
        fn push(&self, title: &str, body: &str) -> Result<()> {
            self.0.lock().unwrap().push((title.into(), body.into()));
            Ok(())
        }
    }

    /// Sink that blocks its first delivery until the test releases it.
    struct BlockingFirstSink {
        first: AtomicBool,
        delivered: mpsc::Sender<String>,
        release: Mutex<mpsc::Receiver<()>>,
    }

    impl NotificationSink for BlockingFirstSink {
        fn push(&self, title: &str, _body: &str) -> Result<()> {
            self.delivered.send(title.into()).unwrap();
            if self.first.swap(false, Ordering::SeqCst) {
                self.release.lock().unwrap().recv().unwrap();
            }
            Ok(())
        }
    }

    #[test]
    fn desktop_notification_has_application_and_message() {
        let notification = desktop_notification("Example title", "Example body");

        assert_eq!(notification.appname, "Montray");
        assert_eq!(notification.summary, "Example title");
        assert_eq!(notification.body, "Example body");
    }

    #[test]
    fn dispatcher_delivers_notifications_in_enqueue_order() {
        let delivered = Arc::new(Mutex::new(Vec::new()));
        let mut dispatcher =
            NotificationDispatcher::start_with_sink(RecordingSink(delivered.clone()), 4).unwrap();

        dispatcher.enqueue("first".into(), "one".into());
        dispatcher.enqueue("second".into(), "two".into());
        dispatcher.enqueue("third".into(), "three".into());
        dispatcher.shutdown();

        assert_eq!(
            *delivered.lock().unwrap(),
            vec![
                ("first".into(), "one".into()),
                ("second".into(), "two".into()),
                ("third".into(), "three".into()),
            ]
        );
    }

    #[test]
    fn dispatcher_drops_each_notification_when_full_and_resumes_afterward() {
        let (delivered_sender, delivered) = mpsc::channel();
        let (release, release_receiver) = mpsc::channel();
        let sink = BlockingFirstSink {
            first: AtomicBool::new(true),
            delivered: delivered_sender,
            release: Mutex::new(release_receiver),
        };
        let mut dispatcher = NotificationDispatcher::start_with_sink(sink, 2).unwrap();

        assert_eq!(
            dispatcher.enqueue_inner("first".into(), String::new()),
            EnqueueOutcome::Queued
        );
        assert_eq!(delivered.recv().unwrap(), "first");
        assert_eq!(
            dispatcher.enqueue_inner("second".into(), String::new()),
            EnqueueOutcome::Queued
        );
        assert_eq!(
            dispatcher.enqueue_inner("third".into(), String::new()),
            EnqueueOutcome::Queued
        );
        assert_eq!(
            dispatcher.enqueue_inner("dropped one".into(), String::new()),
            EnqueueOutcome::Dropped
        );
        assert_eq!(
            dispatcher.enqueue_inner("dropped two".into(), String::new()),
            EnqueueOutcome::Dropped
        );

        release.send(()).unwrap();
        assert_eq!(delivered.recv().unwrap(), "second");
        assert_eq!(
            dispatcher.enqueue_inner("after recovery".into(), String::new()),
            EnqueueOutcome::Queued
        );
        dispatcher.shutdown();

        let remaining: Vec<_> = delivered.try_iter().collect();
        assert_eq!(remaining, vec!["third", "after recovery"]);
    }

    #[test]
    fn shutdown_detaches_a_stuck_notification_worker_after_timeout() {
        let (delivered_sender, delivered) = mpsc::channel();
        let (release, release_receiver) = mpsc::channel();
        let sink = BlockingFirstSink {
            first: AtomicBool::new(true),
            delivered: delivered_sender,
            release: Mutex::new(release_receiver),
        };
        let mut dispatcher = NotificationDispatcher::start_with_sink(sink, 1).unwrap();
        dispatcher.enqueue("blocked".into(), String::new());
        assert_eq!(delivered.recv().unwrap(), "blocked");

        let started = Instant::now();
        assert_eq!(
            dispatcher.shutdown_with_timeout(Duration::from_millis(20)),
            ShutdownOutcome::Detached
        );
        assert!(started.elapsed() < Duration::from_secs(1));

        // Let the detached test worker finish rather than leaking it into later tests.
        release.send(()).unwrap();
    }
}
