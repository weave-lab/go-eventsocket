// Copyright 2013 Alexandre Fiori
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Event Socket client that connects to FreeSWITCH to originate a new call.
package main

import (
	"fmt"
	"log"

	"github.com/weave-lab/go-eventsocket/eventsocket"
)

const uuid = "TEST"

func main() {
	c, err := eventsocket.Dial("10.55.99.131:8021", "ClueCon")

	fmt.Println(c)
	if err != nil {
		log.Fatal(err)
	}
	resp, err := c.Bgapi(fmt.Sprintf("uuid_kill %s", uuid))

	if err != nil {
		fmt.Printf("ERROR: %s", err)
	}

	fmt.Printf("RESPONSE: %+v", resp)
	/*for {
		ev, err := c.ReadEvent()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("\nNew event")
		ev.PrettyPrint()
	}*/
	c.Close()
}
