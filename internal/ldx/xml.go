package ldx

import (
	"encoding/xml"
	"io"
)

// XMLFile is the internal XML representation used for marshaling/unmarshaling.
// This type is private to the internal package and not directly exposed to users.
type XMLFile struct {
	XMLName       xml.Name  `xml:"LDXFile"`
	Locale        string    `xml:"Locale,attr"`
	DefaultLocale string    `xml:"DefaultLocale,attr"`
	Version       string    `xml:"Version,attr"`
	Layers        XMLLayers `xml:"Layers"`
}

type XMLLayers struct {
	Details XMLDetails `xml:"Details"`
}

type XMLDetails struct {
	Strings  []XMLString  `xml:"String"`
	Numerics []XMLNumeric `xml:"Numeric"`
}

type XMLString struct {
	ID    string `xml:"Id,attr"`
	Value string `xml:"Value,attr"`
}

type XMLNumeric struct {
	ID    string `xml:"Id,attr"`
	Value string `xml:"Value,attr"`
	Unit  string `xml:"Unit,attr"`
	DPS   string `xml:"DPS,attr"`
}

// ReadXML decodes XML from a reader and returns the internal structure.
func ReadXML(r io.Reader) (*XMLFile, error) {
	var x XMLFile
	if err := xml.NewDecoder(r).Decode(&x); err != nil {
		return nil, err
	}
	return &x, nil
}

// WriteXML encodes the internal XML structure to a writer.
func WriteXML(w io.Writer, x *XMLFile) error {
	out, err := xml.MarshalIndent(x, "", " ")
	if err != nil {
		return err
	}
	header := xml.Header
	_, err = w.Write(append([]byte(header), out...))
	return err
}
