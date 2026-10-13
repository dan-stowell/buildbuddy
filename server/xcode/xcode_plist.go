package xcode

import (
	"encoding/xml"
	"fmt"
	"io"
)

// The interesting bits to pull from Xcode's version plist.
type xcodePlist struct {
	CFBundleShortVersionString string
	ProductBuildVersion        string
}

func decodeXcodePlist(r io.Reader) (*xcodePlist, error) {
	var document struct {
		XMLName xml.Name    `xml:"plist"`
		Dict    *xcodePlist `xml:"dict"`
	}
	if err := xml.NewDecoder(r).Decode(&document); err != nil {
		return nil, err
	}
	if document.Dict == nil {
		return nil, fmt.Errorf("expected plist dictionary")
	}
	return document.Dict, nil
}

// A plist dictionary alternates keys and values; unknown values may themselves
// contain dictionaries, so skip their entire element.
func (p *xcodePlist) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case xml.EndElement:
			if token.Name == start.Name {
				return nil
			}
		case xml.StartElement:
			if token.Name.Local != "key" {
				return fmt.Errorf("expected plist key, got %s", token.Name.Local)
			}
			var key string
			if err := d.DecodeElement(&key, &token); err != nil {
				return err
			}
			var value xml.StartElement
			for {
				token, err := d.Token()
				if err != nil {
					return err
				}
				if element, ok := token.(xml.StartElement); ok {
					value = element
					break
				}
				if _, ok := token.(xml.EndElement); ok {
					return fmt.Errorf("missing plist value for %s", key)
				}
			}
			var destination *string
			switch key {
			case "CFBundleShortVersionString":
				destination = &p.CFBundleShortVersionString
			case "ProductBuildVersion":
				destination = &p.ProductBuildVersion
			}
			if destination == nil {
				if err := d.Skip(); err != nil {
					return err
				}
			} else {
				if value.Name.Local != "string" {
					return fmt.Errorf("expected plist string for %s, got %s", key, value.Name.Local)
				}
				if err := d.DecodeElement(destination, &value); err != nil {
					return err
				}
			}
		}
	}
}
