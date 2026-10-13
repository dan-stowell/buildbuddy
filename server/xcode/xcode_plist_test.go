package xcode

import (
	"strings"
	"testing"
)

func TestDecodeXcodePlist(t *testing.T) {
	const fixture = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Unknown</key><dict><key>ProductBuildVersion</key><string>ignored</string></dict>
<key>ProductBuildVersion</key><string>16A242</string>
<key>Other</key><array><integer>1</integer><true/></array>
<key>CFBundleShortVersionString</key><string>16.0</string>
<key>Last</key><data>AA==</data>
</dict></plist>`
	p, err := decodeXcodePlist(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if p.CFBundleShortVersionString != "16.0" || p.ProductBuildVersion != "16A242" {
		t.Fatalf("unexpected plist: %+v", p)
	}
}

func TestDecodeXcodePlistErrors(t *testing.T) {
	for _, input := range []string{
		`<plist><string>not a dictionary</string></plist>`,
		`<plist/>`,
		`<plist><dict><key>ProductBuildVersion</key></dict></plist>`,
		`<plist><dict><key>ProductBuildVersion</key><integer>1</integer></dict></plist>`,
		`<plist><dict><string>unpaired</string></dict></plist>`,
		`<plist><dict><key>Unknown</key><dict>`,
	} {
		if _, err := decodeXcodePlist(strings.NewReader(input)); err == nil {
			t.Errorf("expected error for %s", input)
		}
	}
}

func TestDecodeXcodePlistMissingFields(t *testing.T) {
	p, err := decodeXcodePlist(strings.NewReader(`<plist><dict/></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if *p != (xcodePlist{}) {
		t.Fatalf("unexpected plist: %+v", p)
	}
}
