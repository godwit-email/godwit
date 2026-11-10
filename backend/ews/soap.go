package ews

import (
	"encoding/xml"
	"errors"
	"fmt"
)

// The SOAP layer: a thin hand-written one over encoding/xml (ADR 0037's EWS
// exception), one operation per file with its request and response types
// side by side. Requests are written with fixed prefixes; responses are read
// by namespace only, since servers spell the prefixes differently. A
// server's quirk is fixed here, with a test that reproduces it.

const (
	nsSOAP     = "http://schemas.xmlsoap.org/soap/envelope/"
	nsTypes    = "http://schemas.microsoft.com/exchange/services/2006/types"
	nsMessages = "http://schemas.microsoft.com/exchange/services/2006/messages"
)

// requestVersion is the schema every request asks for: Exchange 2016 is the
// oldest server supported.
const requestVersion = "Exchange2016"

// requestEnvelope wraps an operation's request, whose own XMLName is its
// element, such as "m:GetFolder".
type requestEnvelope struct {
	XMLName  xml.Name `xml:"soap:Envelope"`
	SOAP     string   `xml:"xmlns:soap,attr"`
	Types    string   `xml:"xmlns:t,attr"`
	Messages string   `xml:"xmlns:m,attr"`
	Version  struct {
		Version string `xml:"Version,attr"`
	} `xml:"soap:Header>t:RequestServerVersion"`
	Body struct{ Op any } `xml:"soap:Body"`
}

func newRequest(op any) requestEnvelope {
	e := requestEnvelope{SOAP: nsSOAP, Types: nsTypes, Messages: nsMessages}
	e.Body.Op = op
	e.Version.Version = requestVersion
	return e
}

// responseEnvelope reads an operation's response R, the one element of the body.
type responseEnvelope[R any] struct {
	Header struct {
		Version *serverVersion `xml:"http://schemas.microsoft.com/exchange/services/2006/types ServerVersionInfo"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Header"`
	Body struct {
		Fault    *fault `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault"`
		Response R      `xml:",any"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

// serverVersion is the SOAP header's ServerVersionInfo.
type serverVersion struct {
	Major int `xml:"MajorVersion,attr"`
	Minor int `xml:"MinorVersion,attr"`
}

// atLeast2016 reports a version of Exchange 2016 (15.1) or later.
func (v serverVersion) atLeast2016() bool { return v.Major > 15 || v.Major == 15 && v.Minor >= 1 }

// fault is a SOAP fault: the whole request was refused.
type fault struct {
	Code   string `xml:"faultcode"`
	String string `xml:"faultstring"`
	Detail struct {
		Code string `xml:"http://schemas.microsoft.com/exchange/services/2006/types ResponseCode"`
	} `xml:"detail"`
}

func (f *fault) Error() string {
	return fmt.Sprintf("ews: SOAP fault %s %s: %s", f.Code, f.Detail.Code, f.String)
}

// responseMessage is what every operation's per-item response message has.
// Read ResponseMessages with ",any": each operation names its own.
type responseMessage struct {
	Class string `xml:"ResponseClass,attr"` // Success, Warning or Error
	Code  string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseCode"`
	Text  string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages MessageText"`
}

// err is the message's error, nil unless its class is Error.
func (m responseMessage) err() error {
	if m.Class != "Error" {
		return nil
	}
	return &ResponseError{Code: m.Code, Text: m.Text}
}

// ResponseError is the server's refusal of one item of a request.
type ResponseError struct {
	Code string // ResponseCode, such as ErrorItemNotFound
	Text string // MessageText, as the server sent it
}

func (e *ResponseError) Error() string { return "ews: " + e.Code + ": " + e.Text }

// isCode reports that err is a ResponseError with code.
func isCode(err error, code string) bool {
	var re *ResponseError
	return errors.As(err, &re) && re.Code == code
}

// folderID is a FolderId or a DistinguishedFolderId in a request.
type folderID struct {
	XMLName xml.Name
	ID      string `xml:"Id,attr"`
}

func folderRef(id string) folderID {
	return folderID{XMLName: xml.Name{Local: "t:FolderId"}, ID: id}
}

func distinguished(id string) folderID {
	return folderID{XMLName: xml.Name{Local: "t:DistinguishedFolderId"}, ID: id}
}

// itemID is an ItemId in a request.
type itemID struct {
	ID string `xml:"Id,attr"`
}

// fieldURI is a property path in AdditionalProperties.
type fieldURI struct {
	URI string `xml:"FieldURI,attr"`
}

// id is a FolderId or ItemId in a response.
type id struct {
	ID string `xml:"Id,attr"`
}
