package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	httputils "github.com/cascades-fbp/cascades-http/utils"
	"github.com/cascades-fbp/cascades/components/utils"
	"github.com/cascades-fbp/cascades/runtime"
	zmq "github.com/pebbe/zmq4"
)

var (
	urlEndpoint      = flag.String("port.url", "", "Component's input port endpoint")
	methodEndpoint   = flag.String("port.method", "", "Component's input port endpoint")
	headersEndpoint  = flag.String("port.headers", "", "Component's input port endpoint")
	formEndpoint     = flag.String("port.form", "", "Component's input port endpoint")
	responseEndpoint = flag.String("port.resp", "", "Component's output port endpoint")
	bodyEndpoint     = flag.String("port.body", "", "Component's output port endpoint")
	errorEndpoint    = flag.String("port.err", "", "Component's error port endpoint")
	jsonFlag         = flag.Bool("json", false, "Print component documentation in JSON")
	debug            = flag.Bool("debug", false, "Enable debug mode")
)

func assertError(err error) {
	if err != nil {
		fmt.Println("ERROR:", err.Error())
		os.Exit(1)
	}
}

func main() {
	flag.Parse()

	if *jsonFlag {
		doc, _ := registryEntry.JSON()
		fmt.Println(string(doc))
		os.Exit(0)
	}

	if *urlEndpoint == "" || *methodEndpoint == "" {
		flag.Usage()
		os.Exit(1)
	}
	if *responseEndpoint == "" && *bodyEndpoint == "" {
		flag.Usage()
		os.Exit(1)
	}

	log.SetFlags(0)
	if *debug {
		log.SetOutput(os.Stdout)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	var err error

	defer zmq.Term()

	// Url socket
	urlSock, err := utils.CreateInputPort(*urlEndpoint)
	assertError(err)
	defer urlSock.Close()

	// Method socket
	methodSock, err := utils.CreateInputPort(*methodEndpoint)
	assertError(err)
	defer methodSock.Close()

	// Headers socket
	var headersSock *zmq.Socket
	if *headersEndpoint != "" {
		headersSock, err = utils.CreateInputPort(*headersEndpoint)
		assertError(err)
		defer headersSock.Close()
	}
	// Data socket
	var formSock *zmq.Socket
	if *formEndpoint != "" {
		formSock, err = utils.CreateInputPort(*formEndpoint)
		assertError(err)
		defer formSock.Close()
	}

	// Response socket
	var respSock *zmq.Socket
	if *responseEndpoint != "" {
		respSock, err = utils.CreateOutputPort(*responseEndpoint)
		assertError(err)
		defer respSock.Close()
	}
	// Error socket
	var errSock *zmq.Socket
	if *errorEndpoint != "" {
		errSock, err = utils.CreateOutputPort(*errorEndpoint)
		assertError(err)
		defer errSock.Close()
	}
	// Response body socket
	var bodySock *zmq.Socket
	if *bodyEndpoint != "" {
		bodySock, err = utils.CreateOutputPort(*bodyEndpoint)
		assertError(err)
		defer bodySock.Close()
	}

	// Ctrl+C handling
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for _ = range ch {
			log.Println("Give 0MQ time to deliver before stopping...")
			time.Sleep(1e9)
			log.Println("Stopped")
			os.Exit(0)
		}
	}()

	//TODO: setup input ports monitoring to close sockets when upstreams are disconnected

	// Setup socket poll items
	poller := zmq.NewPoller()
	poller.Add(urlSock, zmq.POLLIN)
	poller.Add(methodSock, zmq.POLLIN)
	if headersSock != nil {
		poller.Add(headersSock, zmq.POLLIN)
	}
	if formSock != nil {
		poller.Add(formSock, zmq.POLLIN)
	}

	// This is obviously dangerous but we need it to deal with our custom CA's
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	client.Timeout = 30 * time.Second

	// Main loop
	var (
		ip          [][]byte
		URL, method string
		headers     map[string][]string
		data        url.Values
		request     *http.Request
	)
	log.Println("Started")
	for {
		sockets, err := poller.Poll(-1)
		if err != nil {
			log.Println("Error polling ports:", err.Error())
			continue
		}
		for _, socket := range sockets {
			if socket.Socket == nil {
				log.Println("ERROR: could not find socket in polling items array")
				continue
			}
			ip, err = socket.Socket.RecvMessageBytes(0)
			if err != nil {
				log.Println("Error receiving message:", err.Error())
				continue
			}
			if !runtime.IsValidIP(ip) {
				log.Println("Invalid IP:", ip)
				continue
			}
			switch socket.Socket {
			case urlSock:
				URL = string(ip[1])
				log.Println("URL specified:", URL)
			case methodSock:
				method = strings.ToUpper(string(ip[1]))
				log.Println("Method specified:", method)
			case headersSock:
				err = json.Unmarshal(ip[1], &headers)
				if err != nil {
					log.Println("ERROR: failed to unmarchal headers:", err.Error())
					continue
				}
				log.Println("Headers specified:", headers)
			case formSock:
				err = json.Unmarshal(ip[1], &data)
				if err != nil {
					log.Println("ERROR: failed to unmarchal form data:", err.Error())
					continue
				}
				log.Println("Form specified:", data)
			default:
				log.Println("ERROR: IP from unhandled socket received!")
				continue
			}
		}

		if method == "" || URL == "" || (headersSock != nil && headers == nil) || (formSock != nil && data == nil) {
			continue
		}

		if data != nil {
			request, err = http.NewRequest(method, URL, strings.NewReader(data.Encode()))
		} else {
			request, err = http.NewRequest(method, URL, nil)
		}
		assertError(err)
		for k, v := range headers {
			request.Header.Add(k, v[0])
		}

		response, err := client.Do(request)
		if err != nil {
			log.Printf("ERROR performing HTTP %s %s: %s", request.Method, request.URL, err.Error())
			if errSock != nil {
				errSock.SendMessageDontwait(runtime.NewPacket([]byte(err.Error())))
			}
			method = ""
			URL = ""
			headers = nil
			data = nil
			continue
		}
		resp, err := httputils.Response2Response(response)
		if err != nil {
			log.Printf("ERROR converting response to reply: %s", err.Error())
			if errSock != nil {
				errSock.SendMessageDontwait(runtime.NewPacket([]byte(err.Error())))
			}
			method = ""
			URL = ""
			headers = nil
			data = nil
			continue
		}
		ip, err = httputils.Response2IP(resp)
		if err != nil {
			log.Printf("ERROR converting reply to IP: %s", err.Error())
			if errSock != nil {
				errSock.SendMessageDontwait(runtime.NewPacket([]byte(err.Error())))
			}
			method = ""
			URL = ""
			headers = nil
			data = nil
			continue
		}

		if respSock != nil {
			respSock.SendMessage(ip)
		}
		if bodySock != nil {
			bodySock.SendMessage(runtime.NewPacket(resp.Body))
		}

		method = ""
		URL = ""
		headers = nil
		data = nil
	}
}
