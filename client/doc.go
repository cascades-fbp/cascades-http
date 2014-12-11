package main

import (
	"github.com/cascades-fbp/cascades/library"
)

var registryEntry = &library.Entry{
	Description: "Multi-purpose HTTP client component",
	Elementary:  true,
	Inports: []library.EntryPort{
		library.EntryPort{
			Name:        "URL",
			Type:        "string",
			Description: "URL",
			Required:    true,
		},
		library.EntryPort{
			Name:        "METHOD",
			Type:        "string",
			Description: "HTTP method",
			Required:    true,
		},
		library.EntryPort{
			Name:        "HEADERS",
			Type:        "json",
			Description: "Headers to be added to request",
			Required:    false,
		},
		library.EntryPort{
			Name:        "FORM",
			Type:        "json",
			Description: "Form data to be posted",
			Required:    false,
		},
	},
	Outports: []library.EntryPort{
		library.EntryPort{
			Name:        "RESP",
			Type:        "json",
			Description: "Response JSON object defined in utils of HTTP components library",
			Required:    false,
		},
		library.EntryPort{
			Name:        "BODY",
			Type:        "string",
			Description: "Body of the response",
			Required:    false,
		},
		library.EntryPort{
			Name:        "ERR",
			Type:        "string",
			Description: "Error port for errors while performing requests",
			Required:    false,
		},
	},
}
