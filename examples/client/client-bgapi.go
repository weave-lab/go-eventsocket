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
	if err != nil {
		log.Fatal(err)
	}
	resp, _ := c.Bgapi(fmt.Sprintf("uuid_kill %s %s", uuid))

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
