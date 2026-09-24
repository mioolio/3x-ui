package service

import (
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const maxPolicyRateKbps int64 = 1_000_000_000
const maxOverageMultiplierBps = 1_000_000

func policyInputValue[T any](input *T, current T) T {
	if input != nil {
		return *input
	}
	return current
}

func validatePolicyAction(name, action string) error {
	if action != "stop" && action != "throttle" {
		return fmt.Errorf("%s must be stop or throttle", name)
	}
	return nil
}

func validatePolicyRate(name string, value int64) error {
	if value < 0 || value > maxPolicyRateKbps {
		return fmt.Errorf("%s must be between 0 and %d Kbps", name, maxPolicyRateKbps)
	}
	return nil
}

func validateClientPolicy(input model.Client, existing *model.ClientRecord) error {
	old := model.ClientRecord{
		TotalExhaustAction:         "stop",
		WindowExhaustAction:        "stop",
		TotalOverageMultiplierBps:  model.DefaultOverageMultiplierBps,
		WindowOverageMultiplierBps: model.DefaultOverageMultiplierBps,
	}
	if existing != nil {
		old = *existing
	}
	if old.TotalExhaustAction == "" {
		old.TotalExhaustAction = "stop"
	}
	if old.WindowExhaustAction == "" {
		old.WindowExhaustAction = "stop"
	}
	totalAction := policyInputValue(input.TotalExhaustAction, old.TotalExhaustAction)
	windowAction := policyInputValue(input.WindowExhaustAction, old.WindowExhaustAction)
	if err := validatePolicyAction("totalExhaustAction", totalAction); err != nil {
		return err
	}
	if err := validatePolicyAction("windowExhaustAction", windowAction); err != nil {
		return err
	}
	totalUp := policyInputValue(input.TotalExhaustUpKbps, old.TotalExhaustUpKbps)
	totalDown := policyInputValue(input.TotalExhaustDownKbps, old.TotalExhaustDownKbps)
	windowUp := policyInputValue(input.WindowExhaustUpKbps, old.WindowExhaustUpKbps)
	windowDown := policyInputValue(input.WindowExhaustDownKbps, old.WindowExhaustDownKbps)
	for _, rate := range []struct {
		name  string
		value int64
	}{
		{"totalExhaustUpKbps", totalUp}, {"totalExhaustDownKbps", totalDown},
		{"windowExhaustUpKbps", windowUp}, {"windowExhaustDownKbps", windowDown},
	} {
		if err := validatePolicyRate(rate.name, rate.value); err != nil {
			return err
		}
	}
	if totalAction == "throttle" && (totalUp == 0 || totalDown == 0) {
		return fmt.Errorf("totalExhaustUpKbps and totalExhaustDownKbps must be positive when throttling")
	}
	if windowAction == "throttle" && (windowUp == 0 || windowDown == 0) {
		return fmt.Errorf("windowExhaustUpKbps and windowExhaustDownKbps must be positive when throttling")
	}
	for _, multiplier := range []struct {
		name  string
		value int
	}{
		{"totalOverageMultiplierBps", policyInputValue(input.TotalOverageMultiplierBps, model.EffectiveOverageMultiplierBps(old.TotalOverageMultiplierBps))},
		{"windowOverageMultiplierBps", policyInputValue(input.WindowOverageMultiplierBps, model.EffectiveOverageMultiplierBps(old.WindowOverageMultiplierBps))},
	} {
		if multiplier.value < model.DefaultOverageMultiplierBps || multiplier.value > maxOverageMultiplierBps {
			return fmt.Errorf("%s must be between 10000 and %d", multiplier.name, maxOverageMultiplierBps)
		}
	}
	graceHours := policyInputValue(input.GraceHours, old.GraceHours)
	graceUp := policyInputValue(input.GraceUpKbps, old.GraceUpKbps)
	graceDown := policyInputValue(input.GraceDownKbps, old.GraceDownKbps)
	graceQuota := policyInputValue(input.GraceQuotaBytes, old.GraceQuotaBytes)
	if graceHours < 0 || graceHours > 8760 {
		return fmt.Errorf("graceHours must be between 0 and 8760")
	}
	if err := validatePolicyRate("graceUpKbps", graceUp); err != nil {
		return err
	}
	if err := validatePolicyRate("graceDownKbps", graceDown); err != nil {
		return err
	}
	if graceQuota < 0 {
		return fmt.Errorf("graceQuotaBytes must not be negative")
	}
	if graceHours > 0 && (graceUp == 0 || graceDown == 0) {
		return fmt.Errorf("graceUpKbps and graceDownKbps must be positive when graceHours is set")
	}
	return nil
}
