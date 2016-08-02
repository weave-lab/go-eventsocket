// Copyright 2013 Alexandre Fiori
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// FreeSWITCH Event Socket library for the Go programming language.
//
// eventsocket supports both inbound and outbound event socket connections,
// acting either as a client connecting to FreeSWITCH or as a server accepting
// connections from FreeSWITCH to control calls.
//
// Reference:
// http://wiki.freeswitch.org/wiki/Event_Socket
// http://wiki.freeswitch.org/wiki/Event_Socket_Outbound
//
// WORK IN PROGRESS, USE AT YOUR OWN RISK.
package eventsocket

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const bufferSize = 1024 << 6 // For the socket reader
const eventsBuffer = 16      // For the events channel (memory eater!)
const requestTimeout = 2000

var errMissingAuthRequest = errors.New("Missing auth request")
var errInvalidPassword = errors.New("Invalid password")
var errInvalidCommand = errors.New("Invalid command contains \\r or \\n")

// Connection is the event socket connection handler.
type Connection struct {
	conn          net.Conn
	reader        *bufio.Reader
	textreader    *textproto.Reader
	err           chan error
	cmd, api, evt chan *Event
	hub           *RequestHub
}

type Request struct {
	UUID      string
	timestamp time.Time
	resp      chan string
	command   string
}

type JobResponse struct {
	UUID string
	body string
}

type RequestHub struct {
	requests  map[string]*Request
	inbound   chan *Request
	responses chan *JobResponse
	timeout   chan bool
}

func (h *RequestHub) run() {
	go func() {
		for {
			time.Sleep(1 * time.Second)
			h.timeout <- true
		}
	}()
	go func() {
		for {
			select {
			case request := <-h.inbound:
				//TODO: send to FS event socket, get UUID
				request.UUID = "newUUIDfromFS"
				h.requests[request.UUID] = request
			case response := <-h.responses:
				//TODO: look for UUID in event, and check for request in request object
				fmt.Printf("RESPONSE %+v", response)
				//h.requests[response.UUID].resp <- response.body
				delete(h.requests, response.UUID)

			/*case timeout := <-h.timeout:
				for _, req := range h.requests {
					if (time.Now - req.timestamp) > (time.Millisecond * requestTimeout) {
						req.resp <- "Error: timeout"
					}
				}*/
			}
		}
	}()
}

// newConnection allocates a new Connection and initialize its buffers.
func newConnection(c net.Conn) *Connection {
	h := Connection{
		conn:   c,
		reader: bufio.NewReaderSize(c, bufferSize),
		err:    make(chan error),
		cmd:    make(chan *Event),
		api:    make(chan *Event),
		evt:    make(chan *Event, eventsBuffer),
	}
	hub := &RequestHub{
		requests:  make(map[string]*Request),
		inbound:   make(chan *Request),
		responses: make(chan *JobResponse),
		timeout:   make(chan bool),
	}
	h.hub = hub
	h.hub.run()
	h.textreader = textproto.NewReader(h.reader)
	return &h
}

// HandleFunc is the function called on new incoming connections.
type HandleFunc func(*Connection)

// ListenAndServe listens for incoming connections from FreeSWITCH and calls
// HandleFunc in a new goroutine for each client.
//
// Example:
//
//	func main() {
//		eventsocket.ListenAndServe(":9090", handler)
//	}
//
//	func handler(c *eventsocket.Connection) {
//		ev, err := c.Send("connect") // must always start with this
//		ev.PrettyPrint()             // print event to the console
//		...
//		c.Send("myevents")
//		for {
//			ev, err = c.ReadEvent()
//			...
//		}
//	}
//
func ListenAndServe(addr string, fn HandleFunc) error {
	srv, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	for {
		c, err := srv.Accept()
		if err != nil {
			return err
		}
		h := newConnection(c)
		go h.readLoop()
		go fn(h)
	}
}

// Dial attemps to connect to FreeSWITCH and authenticate.
//
// Example:
//
//	c, _ := eventsocket.Dial("localhost:8021", "ClueCon")
//	ev, _ := c.Send("events plain ALL") // or events json ALL
//	for {
//		ev, _ = c.ReadEvent()
//		ev.PrettyPrint()
//		...
//	}
//
func Dial(addr, passwd string) (*Connection, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	h := newConnection(c)
	m, err := h.textreader.ReadMIMEHeader()
	if err != nil {
		c.Close()
		return nil, err
	}
	if m.Get("Content-Type") != "auth/request" {
		c.Close()
		return nil, errMissingAuthRequest
	}
	fmt.Fprintf(c, "auth %s\r\n\r\n", passwd)
	m, err = h.textreader.ReadMIMEHeader()
	if err != nil {
		c.Close()
		return nil, err
	}
	if m.Get("Reply-Text") != "+OK accepted" {
		c.Close()
		return nil, errInvalidPassword
	}
	go h.readLoop()
	// subscribe to `BACKGROUND_JOB` on connect
	go h.Send("events json BACKGROUND_JOB")
	return h, err
}

// readLoop calls readOne until a fatal error occurs, then close the socket.
func (h *Connection) readLoop() {
	for h.readOne() {
	}
	h.Close()
}

// readOne reads a single event and send over the appropriate channel.
// It separates incoming events from api and command responses.
func (h *Connection) readOne() bool {
	hdr, err := h.textreader.ReadMIMEHeader()
	if err != nil {
		h.err <- err
		return false
	}
	resp := new(Event)
	resp.Header = make(EventHeader)
	if v := hdr.Get("Content-Length"); v != "" {
		length, err := strconv.Atoi(v)
		if err != nil {
			h.err <- err
			return false
		}
		b := make([]byte, length)
		if _, err := io.ReadFull(h.reader, b); err != nil {
			h.err <- err
			return false
		}
		resp.Body = string(b)
	}
	switch hdr.Get("Content-Type") {
	case "command/reply":
		reply := hdr.Get("Reply-Text")
		if strings.HasPrefix(reply, "-E") {
			if len(reply) > 5 {
				h.err <- errors.New(reply[5:])
			} else {
				h.err <- errors.New(reply)
			}
			return true
		}
		if strings.HasPrefix(reply, "%") {
			copyHeaders(&hdr, resp, true)
		} else {
			copyHeaders(&hdr, resp, false)
		}
		h.cmd <- resp
	case "api/response":
		if strings.HasPrefix(resp.Body, "-E") {
			if len(resp.Body) > 5 {
				h.err <- errors.New(resp.Body[5:])
			} else {
				h.err <- errors.New(resp.Body)
			}
			return true
		}
		copyHeaders(&hdr, resp, false)
		h.api <- resp
	case "text/event-plain":
		reader := bufio.NewReader(bytes.NewReader([]byte(resp.Body)))
		resp.Body = ""
		textreader := textproto.NewReader(reader)
		hdr, err = textreader.ReadMIMEHeader()
		if err != nil {
			h.err <- err
			return false
		}
		if v := hdr.Get("Content-Length"); v != "" {
			length, err := strconv.Atoi(v)
			if err != nil {
				h.err <- err
				return false
			}
			b := make([]byte, length)
			if _, err = io.ReadFull(reader, b); err != nil {
				h.err <- err
				return false
			}
			resp.Body = string(b)
		}
		copyHeaders(&hdr, resp, true)
		h.evt <- resp
	case "text/event-json":
		tmp := make(EventHeader)
		err := json.Unmarshal([]byte(resp.Body), &tmp)
		if err != nil {
			fmt.Println("Hi", resp.Body)
			h.err <- err
			return false
		}
		// we dont return `BACKGROUND_JOB` events
		if tmp["Event-Name"] == "BACKGROUND_JOB" {
			j := &JobResponse{
				UUID: tmp["Job-UUID"].(string),
				body: tmp["_body"].(string),
			}
			h.hub.responses <- j
			return true
		}
		// capitalize header keys for consistency.
		for k, v := range tmp {
			resp.Header[capitalize(k)] = v
		}
		if v := resp.Header.Get("_body"); v != "" {
			resp.Body = v
			delete(resp.Header, "_body")
		} else {
			resp.Body = ""
		}
		h.evt <- resp
	case "text/disconnect-notice":
		copyHeaders(&hdr, resp, false)
		h.evt <- resp
	default:
		log.Fatal("Unsupported event:", hdr)
	}
	return true
}

// RemoteAddr returns the remote addr of the connection.
func (h *Connection) RemoteAddr() net.Addr {
	return h.conn.RemoteAddr()
}

// Close terminates the connection.
func (h *Connection) Close() {
	h.conn.Close()
}

// ReadEvent reads and returns events from the server. It supports both plain
// or json, but *not* XML.
//
// When subscribing to events (e.g. `Send("events json ALL")`) it makes no
// difference to use plain or json. ReadEvent will parse them and return
// all headers and the body (if any) in an Event struct.
func (h *Connection) ReadEvent() (*Event, error) {
	var (
		ev  *Event
		err error
	)
	select {
	case err = <-h.err:
		return nil, err
	case ev = <-h.evt:
		return ev, nil
	}
}

// copyHeaders copies all keys and values from the MIMEHeader to Event.Header,
// normalizing header keys to their capitalized version and values by
// unescaping them when decode is set to true.
//
// It's used after parsing plain text event headers, but not JSON.
func copyHeaders(src *textproto.MIMEHeader, dst *Event, decode bool) {
	var err error
	for k, v := range *src {
		if len(v) == 0 {
			continue
		}
		k = capitalize(k)
		if decode {
			dst.Header[k], err = url.QueryUnescape(v[0])
			if err != nil {
				dst.Header[k] = v[0]
			}
		} else {
			dst.Header[k] = v[0]
		}
	}
}

// capitalize capitalizes strings in a very particular manner.
// Headers such as Job-UUID become Job-Uuid and so on. Headers starting with
// Variable_ only replace ^v with V, and headers staring with _ are ignored.
func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] == '_' {
		return s
	}
	ns := bytes.ToLower([]byte(s))
	if len(s) > 9 && s[1:9] == "ariable_" {
		ns[0] = 'V'
		return string(ns)
	}
	toUpper := true
	for n, c := range ns {
		if toUpper {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			ns[n] = c
			toUpper = false
		} else if c == '-' || c == '_' {
			toUpper = true
		}
	}
	return string(ns)
}

// Send sends a single command to the server and returns a response Event.
//
// See http://wiki.freeswitch.org/wiki/Event_Socket#Command_Documentation for
// details.
func (h *Connection) Send(command string) (*Event, error) {
	// Sanity check to avoid breaking the parser
	//if strings.IndexAny(command, "\r\n") > 0 {
	//	return nil, errInvalidCommand
	//}
	fmt.Fprintf(h.conn, "%s\r\n\r\n", command)
	var (
		ev  *Event
		err error
	)
	select {
	case err = <-h.err:
		return nil, err
	case ev = <-h.cmd:
		return ev, nil
	case ev = <-h.api:
		return ev, nil
	}
}

// Bgapi runs a background api request on freeswitch
func (h *Connection) Bgapi(command string, resp chan string) {
	req := &Request{}
	req.resp = resp

}

// MSG is the container used by SendMsg to store messages sent to FreeSWITCH.
// It's supposed to be populated with directives supported by the sendmsg
// command only, like "call-command: execute".
//
// See http://wiki.freeswitch.org/wiki/Event_Socket#sendmsg for details.
type MSG map[string]string

// SendMsg sends messages to FreeSWITCH and returns a response Event.
//
// Examples:
//
//	SendMsg(MSG{
//		"call-command": "hangup",
//		"hangup-cause": "we're done!",
//	}, "", "")
//
//	SendMsg(MSG{
//		"call-command":     "execute",
//		"execute-app-name": "playback",
//		"execute-app-arg":  "/tmp/test.wav",
//	}, "", "")
//
// Keys with empty values are ignored; uuid and appData are optional.
// If appData is set, a "content-length" header is expected (lower case!).
//
// See http://wiki.freeswitch.org/wiki/Event_Socket#sendmsg for details.
func (h *Connection) SendMsg(m MSG, uuid, appData string) (*Event, error) {
	b := bytes.NewBufferString("sendmsg")
	if uuid != "" {
		// Make sure there's no \r or \n in the UUID.
		if strings.IndexAny(uuid, "\r\n") > 0 {
			return nil, errInvalidCommand
		}
		b.WriteString(" " + uuid)
	}
	b.WriteString("\n")
	for k, v := range m {
		// Make sure there's no \r or \n in the key, and value.
		if strings.IndexAny(k, "\r\n") > 0 {
			return nil, errInvalidCommand
		}
		if v != "" {
			if strings.IndexAny(v, "\r\n") > 0 {
				return nil, errInvalidCommand
			}
			b.WriteString(fmt.Sprintf("%s: %s\n", k, v))
		}
	}
	b.WriteString("\n")
	if m["content-length"] != "" && appData != "" {
		b.WriteString(appData)
	}
	if _, err := b.WriteTo(h.conn); err != nil {
		return nil, err
	}
	var (
		ev  *Event
		err error
	)
	select {
	case err = <-h.err:
		return nil, err
	case ev = <-h.cmd:
		return ev, nil
	}
}

// Execute is a shortcut to SendMsg with call-command: execute without UUID,
// suitable for use on outbound event socket connections (acting as server).
//
// Example:
//
//	Execute("playback", "/tmp/test.wav", false)
//
// See http://wiki.freeswitch.org/wiki/Event_Socket#execute for details.
func (h *Connection) Execute(appName, appArg string, lock bool) (*Event, error) {
	var evlock string
	if lock {
		// Could be strconv.FormatBool(lock), but we don't want to
		// send event-lock when it's set to false.
		evlock = "true"
	}
	return h.SendMsg(MSG{
		"call-command":     "execute",
		"execute-app-name": appName,
		"execute-app-arg":  appArg,
		"event-lock":       evlock,
	}, "", "")
}

// ExecuteUUID is similar to Execute, but takes a UUID and no lock. Suitable
// for use on inbound event socket connections (acting as client).
func (h *Connection) ExecuteUUID(uuid, appName, appArg string) (*Event, error) {
	return h.SendMsg(MSG{
		"call-command":     "execute",
		"execute-app-name": appName,
		"execute-app-arg":  appArg,
	}, uuid, "")
}

// EventHeader represents events as a pair of key:value.
type EventHeader map[string]interface{}

func (r EventHeader) Get(key string) string {
	if v, ok := r[key].(string); ok {
		return v
	}
	return ""
}

// return slice of variables (created when using `push` action in FreeSWITCH)
func (r EventHeader) GetSlice(key string) []string {
	var s []string
	if v, ok := r[key]; ok {
		a, b := v.([]interface{})
		if b == true {
			for _, d := range a {
				s = append(s, d.(string))
			}
		} else {
			s = append(s, r.Get(key))
		}
	}
	return s
}

// Event represents a FreeSWITCH event.
type Event struct {
	Header EventHeader // Event headers, key:val
	Body   string      // Raw body, available in some events
}

func (r *Event) String() string {
	if r.Body == "" {
		return fmt.Sprintf("%s", r.Header)
	} else {
		return fmt.Sprintf("%s body=%s", r.Header, r.Body)
	}
}

// Get returns an Event value, or "" if the key doesn't exist.
func (r *Event) Get(key string) string {
	return r.Header.Get(key)
}

// Get returns an Event value, or "" if the key doesn't exist.
func (r *Event) GetSlice(key string) []string {
	return r.Header.GetSlice(key)
}

// GetInt returns an Event value converted to int, or an error if conversion
// is not possible.
func (r *Event) GetInt(key string) (int, error) {
	n, err := strconv.Atoi(r.Get(key))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// PrettyPrint prints Event headers and body to the standard output.
func (r *Event) PrettyPrint() {
	var keys []string
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s: %#v\n", k, r.Header[k])
	}
	if r.Body != "" {
		fmt.Printf("BODY: %#v\n", r.Body)
	}
}
