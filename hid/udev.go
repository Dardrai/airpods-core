package hid

import _ "embed"

// UdevRules is the udev rule file that lets the logged-in user open the dongle on Linux. The apps
// package their own copy of 70-airpods-hid.rules, and can test that copy against this one.
//
//go:embed 70-airpods-hid.rules
var UdevRules string
