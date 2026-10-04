# Changelog

## [2.0.0](https://github.com/dimonomid/salmon/compare/v1.2.0...v2.0.0) (2026-10-04)


### ⚠ BREAKING CHANGES

* migrate Go module path to /v2
* rename project to Montray

### Features

* add migration from Salmon installations ([6dbf571](https://github.com/dimonomid/salmon/commit/6dbf571237ff3955d87a794c456b36589e98b39f))
* rename project to Montray ([bb02c41](https://github.com/dimonomid/salmon/commit/bb02c416c62b606df907790d5d7b07c1b8a436d0))
* Update app icons to heart ([3a87e56](https://github.com/dimonomid/salmon/commit/3a87e568fc062c97be1a4ba3997094cbe6dc6895))


### Bug Fixes

* allow server setup from temporary executable ([76e6a56](https://github.com/dimonomid/salmon/commit/76e6a56efa70ea4cb01f347df46e97c3a4856310))


### Miscellaneous Chores

* migrate Go module path to /v2 ([d5d6184](https://github.com/dimonomid/salmon/commit/d5d618440e6981e7cafcb521658743f626724d59))

## [1.2.0](https://github.com/dimonomid/salmon/compare/v1.1.1...v1.2.0) (2026-09-15)


### Features

* install salmon binary during setup ([519da2e](https://github.com/dimonomid/salmon/commit/519da2e86e7f8d9a28050601920ea3d5d7b685b8))
* show sudo guidance for privileged setup commands ([88df66d](https://github.com/dimonomid/salmon/commit/88df66d0bcead2075f75988a383dedd0cb8eef31))
* Start salmon service during setup ([67dc23b](https://github.com/dimonomid/salmon/commit/67dc23b1846f60e80bd95d497165995b96ce21fd))


### Bug Fixes

* **build:** build Linux salmon-watch on Ubuntu 20.04 ([3d5e018](https://github.com/dimonomid/salmon/commit/3d5e018bbcf59cda16c0ead420f399af96e0e3c2))
* **build:** Disable CGO for portable salmon binaries ([491f037](https://github.com/dimonomid/salmon/commit/491f037221ce591034883b6e63d88728fca421e7))

## [1.1.1](https://github.com/dimonomid/salmon/compare/v1.1.0...v1.1.1) (2026-09-15)


### Bug Fixes

* **salmon-watch:** suggest setup when default config is missing ([eef2476](https://github.com/dimonomid/salmon/commit/eef2476fca0affada566d1e969653d17836ef820))
* suggest config-only setup as well when config is missing ([87231ff](https://github.com/dimonomid/salmon/commit/87231ff0ec8ef3fc205ee1ea370ca2fa2439f209))

## [1.1.0](https://github.com/dimonomid/salmon/compare/v1.0.0...v1.1.0) (2026-09-14)


### Features

* Add "Restart and reload configuration" to tray menu ([c97d75c](https://github.com/dimonomid/salmon/commit/c97d75c909266406a0edae79aecb79e0844a2868))
* add connection timeout and improve incident wording ([09ae0ea](https://github.com/dimonomid/salmon/commit/09ae0ea2997e89fe15a03d8f86a97d87a8885ade))
* Add debug makefile target ([dadf281](https://github.com/dimonomid/salmon/commit/dadf281755285657d323c9e53bedf39733cb9ae7))
* **ci:** Implement e2e test for the new Rust salmon-watch ([ac59b55](https://github.com/dimonomid/salmon/commit/ac59b55a8344fe2bda89949ce17006ade40f446d))
* Implement --version in Rust salmon-watch ([25f1724](https://github.com/dimonomid/salmon/commit/25f17247587d4d5bed029f7359100a24c7ea69b8))
* Implement actual comms with salmon server ([09a3687](https://github.com/dimonomid/salmon/commit/09a36878dc56241906ffc994a4a725fae2d42828))
* Improve Rust client app icon ([03150cd](https://github.com/dimonomid/salmon/commit/03150cd87cb86f865f5eff6a97c96ffb0e5db093))
* in Rust client setup command, autodetect old launcher ([74c53f2](https://github.com/dimonomid/salmon/commit/74c53f2b7694609b0e4f007359bcc80b94411d52))
* In Rust client, add better shutdown and logging ([b664648](https://github.com/dimonomid/salmon/commit/b66464844d3eb39d8549377defd9b80290286a27))
* In rust client, add more tests, implement timestamps coloring ([c2358b4](https://github.com/dimonomid/salmon/commit/c2358b4991fa3a9952d5f8e32ff79866149adb9c))
* In Rust client, bring app to foreground ([c9810b7](https://github.com/dimonomid/salmon/commit/c9810b726e4e70addc8944c2ca382089a7e7565b))
* In Rust client, implement better logging ([0f26155](https://github.com/dimonomid/salmon/commit/0f26155f035c620c6a5140281022d015a760c8c9))
* In Rust client, implement setup command ([0fe2c9b](https://github.com/dimonomid/salmon/commit/0fe2c9b42f458103565ce31c498ef13a0eaae039))
* In Rust client, implement ssh tunnel ([33d6c4d](https://github.com/dimonomid/salmon/commit/33d6c4df0bde716f8b7cf80f21445bb769abb11f))
* In Rust client, implement TLS + bearer auth ([97bd9e8](https://github.com/dimonomid/salmon/commit/97bd9e8ec19a12279ccec90c10145285da49177b))
* In Rust client, improve app icon ([aceaf99](https://github.com/dimonomid/salmon/commit/aceaf992d4d27747e81af804a1d237b887062b51))
* In Rust client, make UI a bit nicer ([6ca6d3b](https://github.com/dimonomid/salmon/commit/6ca6d3be7ea09c9b50cadd8187b2d3e4213ac9d1))
* In Rust client, preserve window geometry ([c019d6c](https://github.com/dimonomid/salmon/commit/c019d6c4efc4401c068f0eb8b9bb6f245bd12810))
* In Rust client, reduce title padding ([b54db2d](https://github.com/dimonomid/salmon/commit/b54db2d7a985e7307dc9aa16b3a7ba9feef6e22a))
* In Rust client, use slightly larger fonts ([91a06bc](https://github.com/dimonomid/salmon/commit/91a06bc4c92eef40e925c55fe3e56a4805a04795))
* In Rust client, wrap text as much as we can ([68f0dd4](https://github.com/dimonomid/salmon/commit/68f0dd43e5c6d5433dcd149380d82db9e9c8246b))
* In Rust clients, round corners of a snoozed incident ([6d030a3](https://github.com/dimonomid/salmon/commit/6d030a342b1cf4326642ad254068d08eb9ed51e7))
* in Rust launcher, add X- marker ([4ada885](https://github.com/dimonomid/salmon/commit/4ada88566ae50585fbbe803c9b864c824af0320b))
* Make local addr optional with ssh tunnel ([9d5d0e1](https://github.com/dimonomid/salmon/commit/9d5d0e12cb515dec4a7e0a2013bea8e0539aabf2))
* Migrate Rust salmon-watch to clap for arg parsing ([f8c656a](https://github.com/dimonomid/salmon/commit/f8c656ae69b21922e39e2321ae1f08d594c25ad9))
* optimize Rust client binary for size ([c1b1b98](https://github.com/dimonomid/salmon/commit/c1b1b9816c7e9cf9803d71fc16b13ea2abe14030))
* Rename salmon-watch to salmon-watch-legacy ([468985d](https://github.com/dimonomid/salmon/commit/468985d08eac73a3ef0956bdfc37ecb75e5bc908))
* Rename salmon-watch-2 to salmon-watch ([1e5ae8f](https://github.com/dimonomid/salmon/commit/1e5ae8fa9815d5e225ceb4588489c44101ca2aa4))
* Retry failed persistence less frequently ([a8cb86e](https://github.com/dimonomid/salmon/commit/a8cb86e943824ac7f9f397c5291712028993fd33))
* Rust salmon-client UI draft ([073b11a](https://github.com/dimonomid/salmon/commit/073b11a1388c20fa4b89d6da2e30bff88061a2f6))
* Send notification when snooze expires ([81da57b](https://github.com/dimonomid/salmon/commit/81da57be8c8b97135aa541e9f6062e6506920a2b))
* Set section header color properly ([15690fe](https://github.com/dimonomid/salmon/commit/15690fed6a3d64b6fa919ee788f9ec39526ac48d))
* Split legacy and new salmon-watch state files safely ([5295553](https://github.com/dimonomid/salmon/commit/52955534e52d09e7be61673b8969e1f29e78e059))
* Support custom tunnel commands in Rust salmon-watch ([7213181](https://github.com/dimonomid/salmon/commit/7213181adfe2900ae56e34866922729e42d3e8b1))
* Support forced exit on second SIGINT in Rust salmon-watch ([6d7f901](https://github.com/dimonomid/salmon/commit/6d7f90124d47e1bf77794ad8a41d8251bffbe9ab))


### Bug Fixes

* Bring Rust client time format on par with Go client ([579c1bb](https://github.com/dimonomid/salmon/commit/579c1bbeddfe0b3f8dadef978362280889572851))
* Don't build Rust client on the default makefile target ([2b93bc7](https://github.com/dimonomid/salmon/commit/2b93bc7ba8f628ecbb3f137aa92c01758451a19c))
* Don't use notification urgency to unbreak macos build ([f415ce8](https://github.com/dimonomid/salmon/commit/f415ce88fcbc299413a907a703624b3141ec39ac))
* Error out on empty servers list in Rust salmon-watch ([d449bb7](https://github.com/dimonomid/salmon/commit/d449bb79c97586e16073803829ff7eff48e7b232))
* Fix Makefile after renaming Rust binary ([00c96fd](https://github.com/dimonomid/salmon/commit/00c96fda71798b26964f42d6b171d800324f94dd))
* Get rid of flickering when restoring GUI window ([13843a7](https://github.com/dimonomid/salmon/commit/13843a7d70a566a04e29679460076605f1409dd8))
* Handle not only SIGINT but also SIGTERM ([2bafa74](https://github.com/dimonomid/salmon/commit/2bafa74003a4ec285a46734b43365d01f120f7e6))
* harden tunnel subprocess cleanup in Rust salmon-watch ([7b1d292](https://github.com/dimonomid/salmon/commit/7b1d292045905926794876dc786fde17f8349ec1))
* In Rust client, bring GUI window to focus ([0b67463](https://github.com/dimonomid/salmon/commit/0b67463788b34f110c15d22cdb12fdf29ce15e30))
* In Rust client, don't reuse assets from Go ([6ccec3b](https://github.com/dimonomid/salmon/commit/6ccec3bc4e51c77bb0e5b992f7d5e0c4d2d2d5e6))
* In Rust client, fix snoozing internal incidents ([c5b5879](https://github.com/dimonomid/salmon/commit/c5b587983d905eb6268d316a4197dee3630b4435))
* In Rust client, fix tray icon flashing cadence ([28e923f](https://github.com/dimonomid/salmon/commit/28e923f7293b87a481d0ea339be50fda2941a113))
* In Rust client, move separator out of incident card ([3ace2ae](https://github.com/dimonomid/salmon/commit/3ace2ae3025c077d31e0471bdae45d5270fd4052))
* In Rust client, prevent icon from taking up horizontal space ([980559a](https://github.com/dimonomid/salmon/commit/980559a8a2e356bde53ee8bb404b61c1b68acda6))
* In Rust salmon-watch, persist snooze changes before committing ([0323bdd](https://github.com/dimonomid/salmon/commit/0323bddef9fb9e3ce6f9d45ccee94affd9be878b))
* in Rust watch, bound and serialize desktop notifications ([9c90313](https://github.com/dimonomid/salmon/commit/9c903138dfb1d9ea16abce7d8fccd3c701feef29))
* **ui:** Use more meaningful default size for GUI window ([dc2fd65](https://github.com/dimonomid/salmon/commit/dc2fd65328d77b6f1ab64afbaa75d649d503615c))

## 1.0.0 (2026-09-05)


### ⚠ BREAKING CHANGES

* Hide all setup-related commands in salmon-watch under setup
* Hide all setup-related commands under setup
* **systemd:** systemd rules must use `names` instead of `name`. That's fine since there are no users yet other than myself.
* collector YAML configurations must use `conditions` instead of `conds`. This is fine since there are no users yet besides myself.
* YAML and JSON field names have changed without backward compatibility. This is fine because there are no users yet other than myself.
* the item JSON field "comment" is now "details", and the exec YAML field "comment" is now "description". It's ok since there are no users yet other than myself.

### Features

* add guided setup commands for salmon and salmon-watch ([09f31c3](https://github.com/dimonomid/salmon/commit/09f31c3706371b77f82544cbc017a280fb80ca17))
* Add icon to the application entry ([51972f3](https://github.com/dimonomid/salmon/commit/51972f34217bbb5a86b326b3f9545fb2dc5f5061))
* Add logs for pending resolution ([00d8651](https://github.com/dimonomid/salmon/commit/00d8651cdae070eff77e74e2161fd3232dea99b9))
* Add shutdown logs in salmon-watch ([49d9ea2](https://github.com/dimonomid/salmon/commit/49d9ea2cee4faa794e3d0fc03f36b2e725e5446f))
* Add structured application logging ([a10ed29](https://github.com/dimonomid/salmon/commit/a10ed29201f4c8887f89a2d320057a8a2d326d2b))
* Add support for ssh tunnel in salmon-monitor ([0ddfb2d](https://github.com/dimonomid/salmon/commit/0ddfb2d7ad0175647bd500e31e74ec037b4e4abd))
* add TLS support for WebSocket connections ([71a383f](https://github.com/dimonomid/salmon/commit/71a383f701494eab627b2fa7134d2b3aaff56c88))
* **auth:** add bearer token authentication ([4c45cf9](https://github.com/dimonomid/salmon/commit/4c45cf92bd1a7c498800052a361b6618ec2573ec))
* Capture output of exec-ed commands ([b8592eb](https://github.com/dimonomid/salmon/commit/b8592eba3227696244544d66bc655a9e2cd0e9a3))
* Hide all setup-related commands in salmon-watch under setup ([db41c86](https://github.com/dimonomid/salmon/commit/db41c8696d8ffe58140ef26bcae8f0dad7c81359))
* Hide all setup-related commands under setup ([daa0bc6](https://github.com/dimonomid/salmon/commit/daa0bc6a99f837188b15f32dace072f00f06eb97))
* Hide the list of snooze durations behind a Snooze button ([1990bdc](https://github.com/dimonomid/salmon/commit/1990bdc396df4b12f646087121d9bfbe91238a32))
* Implement forgetting stale incidents ([a367638](https://github.com/dimonomid/salmon/commit/a367638ff139197986ceead6c38d34beed0f4fb6))
* Implement timeout for exec commands ([a8430a8](https://github.com/dimonomid/salmon/commit/a8430a8f71b95070d39f55606b002b8aee25f86e))
* Improve icon a little bit ([dc5c581](https://github.com/dimonomid/salmon/commit/dc5c5812381f73210ff20b6d87cffb4c0011b7e1))
* mass rename of misnomers in configs and json protocols ([8602266](https://github.com/dimonomid/salmon/commit/860226642a65607d1cd1ed2f23ef5ea14afd236c))
* rename collector conds to conditions ([dbc5612](https://github.com/dimonomid/salmon/commit/dbc5612e003377f8e46c484271ee03ecf8ff418a))
* rename incident comments to details ([889531b](https://github.com/dimonomid/salmon/commit/889531b29a19a90f7452e36e2baf5466299c6fc1))
* **salmon-watch:** keep status unknown until servers respond ([f8b8a62](https://github.com/dimonomid/salmon/commit/f8b8a622510738802862a30b32c61ef5afe106b0))
* **salmon-watch:** Make setup create application entry as well ([10e3c86](https://github.com/dimonomid/salmon/commit/10e3c860daabff63ea7b8ab083ef0ccb2bd9c3a7))
* **salmon-watch:** show initialization progress in tray icon ([f872e5c](https://github.com/dimonomid/salmon/commit/f872e5ce13beceffee68ce4be8dd5026d6cb2f55))
* **systemd:** allow multiple unit names per rule ([4c9e2f1](https://github.com/dimonomid/salmon/commit/4c9e2f17153aaee2f70f366d712e9896baecddc1))
* **systemd:** detect services stuck in restart loops ([2affb64](https://github.com/dimonomid/salmon/commit/2affb64ec08f825ffbace71c99466fb6b123f453))
* Update incident details with resolution status ([661ca66](https://github.com/dimonomid/salmon/commit/661ca66e32d4a61e7c5781575011088f0878084b))
* use linear reconnect backoff ([5b0f56d](https://github.com/dimonomid/salmon/commit/5b0f56dda26ca85d75a76499d27edce105d0de9e))
* Use salmon user and group for the systemd service ([4e56328](https://github.com/dimonomid/salmon/commit/4e56328b16f56a07c11841256a1d4d478348fbfc))


### Bug Fixes

* apply backpressure instead of dropping state updates ([99e42fb](https://github.com/dimonomid/salmon/commit/99e42fbf3fce91b4d1f855c116afea627a3c5732))
* **core:** make messenger backpressure shutdown-aware ([5f9cd3d](https://github.com/dimonomid/salmon/commit/5f9cd3d758bb5b6f9b722324136072b039083c66))
* harden backend lifecycle and configuration handling ([3cced3b](https://github.com/dimonomid/salmon/commit/3cced3bb6219296a4451f872976410906fbedecd))
* Improve systemd service description ([d0b3520](https://github.com/dimonomid/salmon/commit/d0b352038c0e2abaecbff6aef8b64d34e84bfa48))
* In salmon-watch, add server_id tag to the logs ([5e8a7fa](https://github.com/dimonomid/salmon/commit/5e8a7fa4c2854069254d5b48a74f2a68f274e47c))
* Isolate tunnel subprocess ot a process group ([33a1c87](https://github.com/dimonomid/salmon/commit/33a1c87f065818d2869345b2f3e33f63a0da0b64))
* Make setup commands more cautious ([e57a338](https://github.com/dimonomid/salmon/commit/e57a3385567f1639fc089fd3422edf6401ce03fc))
* **make:** avoid invoking Go for executable suffix ([14c35ba](https://github.com/dimonomid/salmon/commit/14c35ba5809e7a480044bfec04b10274465ea55d))
* reject unexpected positional command arguments ([d04041e](https://github.com/dimonomid/salmon/commit/d04041e160e86add31e4a948e0a9538504380263))
* Remove PrivateTmp from systemd service ([911dd21](https://github.com/dimonomid/salmon/commit/911dd21ef2ae368f9fa9e764ebffa193e2a01339))
* Remove the notification on salmon-watch startup ([c49e714](https://github.com/dimonomid/salmon/commit/c49e7145946cb8a94feb8ba656d58251de26cafe))
* **salmon-watch:** avoid global HTTP server state ([0962c3e](https://github.com/dimonomid/salmon/commit/0962c3e3acd3b674138db39e21771cbf1026c505))
* **salmon-watch:** bind status API to loopback ([2f2c04e](https://github.com/dimonomid/salmon/commit/2f2c04e52e3075265279d79d8d318acb7419d172))
* **salmon-watch:** close status WebSockets on shutdown ([c58f74c](https://github.com/dimonomid/salmon/commit/c58f74c106281b33d211ef5dc6b44758caa33758))
* **salmon-watch:** expire snoozes across system suspend ([32cf30f](https://github.com/dimonomid/salmon/commit/32cf30f6b3a7afbfce2d9b9bb2d446c553dc0a04))
* **salmon-watch:** preserve tray icon flash cadence ([7910de1](https://github.com/dimonomid/salmon/commit/7910de14f91a4d59d505f2b4810b69adbb613b14))
* **salmon-watch:** reject setup on unsupported platforms ([740bb58](https://github.com/dimonomid/salmon/commit/740bb582e14b3bc4d54cfa7c54310f7b1eafc95b))
* **salmon-watch:** stop incident-state worker on close ([c574b77](https://github.com/dimonomid/salmon/commit/c574b77805b08c5c05555b56a204bb41e24686cf))
* **salmon-watch:** validate server IDs ([7c37ef7](https://github.com/dimonomid/salmon/commit/7c37ef7a17faf4187394440c1abd6858eaad59b4))
* Serialize all the wsclient events ([7b59e79](https://github.com/dimonomid/salmon/commit/7b59e79fe3ec3fcce4c9d44a1291aeddca4fec00))
* **service:** always restart Salmon service ([92870e1](https://github.com/dimonomid/salmon/commit/92870e16b4649f915e72eaa2654fd15a5197bdc0))
* **service:** keep retrying after startup failures ([223298c](https://github.com/dimonomid/salmon/commit/223298c9bbdddb7e9b82998ccab424757abd7cfe))
* shell-escape setup command hints ([f75c25e](https://github.com/dimonomid/salmon/commit/f75c25eecd67d11a1661f266cfe967055dac5fb5))
* Shutdown cleanly on Ctrl+C in salmon-watch ([df7f3ec](https://github.com/dimonomid/salmon/commit/df7f3ec66280fc68e2f237eee95ac179281d56b8))
* **statestracker:** publish immutable incident snapshots ([364ba75](https://github.com/dimonomid/salmon/commit/364ba75411a92301e959f7f448dc7b9189fa212c))
* **systemd:** make blocked provider sends interruptible ([f53cef0](https://github.com/dimonomid/salmon/commit/f53cef08018beb670c973f62ea236c10dbaa5e3f))
* Treat not-sent-by-systemd as resolving state in default config ([5ee784a](https://github.com/dimonomid/salmon/commit/5ee784aba9a5d45a2a94f2fcc92a5c6eabc55c70))
* **ui:** preserve open snooze menu across updates ([503a3a4](https://github.com/dimonomid/salmon/commit/503a3a466409a91fd6ea7cbfa2c716fbe1b9b059))
* **watch:** harden WebSocket messages handling. ([b2af958](https://github.com/dimonomid/salmon/commit/b2af958dab55bdaae2f6cfe6fc46d5388bc69e54))
* **webserver:** disconnect slow WebSocket consumers ([c8c86be](https://github.com/dimonomid/salmon/commit/c8c86be4cff9e3d9189656ad95a7089fe8d9afff))
* **wsclient:** apply backpressure to connection events ([2c052a4](https://github.com/dimonomid/salmon/commit/2c052a4252b7164ec2689d077bfc90abd0e108d7))
* **wsclient:** disconnect on malformed server messages ([e4788de](https://github.com/dimonomid/salmon/commit/e4788de19188ecb4d7e67f95e64158e8337d0a50))
