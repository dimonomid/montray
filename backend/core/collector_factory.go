package core

import (
	"fmt"

	"github.com/dimonomid/montray/backend/collectors"
	"github.com/dimonomid/montray/backend/collectors/exec"
	"github.com/dimonomid/montray/backend/collectors/systemd"
)

func createCollector(
	cfg Collector, commonParams collectors.Params,
) (collectors.Collector, error) {
	if err := CheckID(cfg.ID); err != nil {
		return nil, err
	}

	numTypes := 0
	if cfg.Systemd != nil {
		numTypes++
	}
	if cfg.Exec != nil {
		numTypes++
	}
	if numTypes != 1 {
		return nil, fmt.Errorf("config contains %d collector types; exactly one collector type is required", numTypes)
	}

	if cfg.Systemd != nil {
		c, err := systemd.NewCollector(systemd.CollectorParams{
			Common: commonParams,
			Config: *cfg.Systemd,
		})
		if err != nil {
			return nil, fmt.Errorf("creating systemd collector: %w", err)
		}

		return c, nil
	}

	if cfg.Exec != nil {
		c, err := exec.NewCollector(exec.CollectorParams{
			Common: commonParams,
			Config: *cfg.Exec,
		})
		if err != nil {
			return nil, fmt.Errorf("creating exec collector: %w", err)
		}

		return c, nil
	}

	return nil, fmt.Errorf("no valid collector configuration")
}

func createCollectors(
	cfgs []Collector, commonParams collectors.Params,
) ([]collectors.Collector, error) {
	ret := make([]collectors.Collector, 0, len(cfgs))

	usedIDs := make(map[string]struct{}, len(cfgs))

	for i, cfg := range cfgs {
		if _, used := usedIDs[cfg.ID]; used {
			// Found duplicate collector ID, gotta error out, but first we need to
			// clean up: roll back collectors created from earlier entries before
			// returning the configuration error, otherwise those workers would be
			// leaked.
			for _, collector := range ret {
				collector.Close()
			}
			return nil, fmt.Errorf("collector config #%d: duplicate id %q", i, cfg.ID)
		}

		usedIDs[cfg.ID] = struct{}{}

		curCommonParams := commonParams
		curCommonParams.ID = cfg.ID
		curCommonParams.Logger = commonParams.Logger.WithContext("collector", cfg.ID)
		c, err := createCollector(cfg, curCommonParams)
		if err != nil {
			// Creating this entry failed after earlier collectors had already
			// started, so close those collectors as construction rollback.
			for _, collector := range ret {
				collector.Close()
			}
			return nil, fmt.Errorf("creating collector from config #%d: %w", i, err)
		}

		ret = append(ret, c)
	}

	return ret, nil
}
