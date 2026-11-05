package hid

import (
	"fmt"
	"strings"
	"testing"
)

func TestUdevRulesCoverEveryProduct(t *testing.T) {
	if strings.Contains(UdevRules, "\r") {
		t.Error("carriage returns would break the installed file")
	}
	if !strings.HasSuffix(UdevRules, "\n") {
		t.Error("no final newline")
	}
	for _, product := range []uint16{ProductAudio, ProductControl} {
		rule := fmt.Sprintf(`SUBSYSTEM=="hidraw", ATTRS{idVendor}=="%04x", ATTRS{idProduct}=="%04x", TAG+="uaccess"`, VendorID, product)
		if !strings.Contains(UdevRules, rule+"\n") {
			t.Errorf("no rule for %04x", product)
		}
	}
}
