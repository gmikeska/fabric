/*
Copyright Digital Asset Holdings, LLC 2016 All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hyperledger/fabric/orderer/common/bootstrap/provisional"
	cb "github.com/hyperledger/fabric/protos/common"
	ab "github.com/hyperledger/fabric/protos/orderer"
	"github.com/op/go-logging"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

var logger = logging.MustGetLogger("sbft_test")

var UPDATE byte = 0
var SEND byte = 1

var NEEDED_UPDATES = 2
var NEEDED_SENT = 1

func TestSbftPeer(t *testing.T) {
	t.Parallel()
	skipInShortMode(t)
	tempDir, err := ioutil.TempDir("", "sbft_test")
	if err != nil {
		panic("Failed to create a temporary directory")
	}
	// We only need the path as the directory will be created
	// by the peer - TODO: modify sbft to tolerate an existing
	// directory
	os.RemoveAll(tempDir)
	defer func() {
		os.RemoveAll(tempDir)
	}()
	c := flags{init: "testdata/config.json",
		genesisFile: fmt.Sprintf("%s_%s", tempDir, "genesis"),
		listenAddr:  ":6101",
		grpcAddr:    ":7101",
		certFile:    "testdata/cert1.pem",
		keyFile:     "testdata/key.pem",
		dataDir:     tempDir}

	logger.Info("Initialization of instance.")
	err = initInstance(c)
	if err != nil {
		t.Errorf("Initialization failed: %s", err)
		return
	}
	logging.SetLevel(logging.DEBUG, "")

	logger.Info("Starting an instance in the background.")
	go serve(c)
	<-time.After(5 * time.Second)

	logger.Info("Creating an Atomic Broadcast GRPC connection.")
	timeout := 4 * time.Second
	clientconn, err := grpc.Dial(":7101", grpc.WithBlock(), grpc.WithTimeout(timeout), grpc.WithInsecure())
	if err != nil {
		t.Errorf("Failed to connect to GRPC: %s", err)
		return
	}
	client := ab.NewAtomicBroadcastClient(clientconn)

	resultch := make(chan byte)
	errorch := make(chan error)

	logger.Info("Starting a goroutine waiting for ledger updates.")
	go updateReceiver(t, resultch, errorch, client)

	logger.Info("Starting a single broadcast sender goroutine.")
	go broadcastSender(t, resultch, errorch, client)

	checkResults(t, resultch, errorch)
}

func checkResults(t *testing.T, resultch chan byte, errorch chan error) {
	l := len(errorch)
	for i := 0; i < l; i++ {
		errres := <-errorch
		t.Error(errres)
	}

	updates := 0
	sentBroadcast := 0
	for i := 0; i < 3; i++ {
		select {
		case result := <-resultch:
			switch result {
			case UPDATE:
				updates++
			case SEND:
				sentBroadcast++
			}
		case <-time.After(30 * time.Second):
			continue
		}
	}
	if updates != NEEDED_UPDATES {
		t.Errorf("We did not get all the ledger updates.")
	} else if sentBroadcast != NEEDED_SENT {
		t.Errorf("We were unable to send all the broadcasts.")
	} else {
		logger.Info("Successfully sent and received everything.")
	}
}

func updateReceiver(t *testing.T, resultch chan byte, errorch chan error, client ab.AtomicBroadcastClient) {
	logger.Info("{Update Receiver} Creating a ledger update delivery stream.")
	dstream, err := client.Deliver(context.Background())
	if err != nil {
		errorch <- fmt.Errorf("Failed to get Deliver stream: %s", err)
		return
	}
	err = dstream.Send(&ab.SeekInfo{
		ChainID:  provisional.TestChainID,
		Start:    &ab.SeekPosition{Type: &ab.SeekPosition_Newest{Newest: &ab.SeekNewest{}}},
		Stop:     &ab.SeekPosition{Type: &ab.SeekPosition_Specified{Specified: &ab.SeekSpecified{Number: ^uint64(0)}}},
		Behavior: ab.SeekInfo_BLOCK_UNTIL_READY,
	})
	if err != nil {
		errorch <- fmt.Errorf("Failed to send to Deliver stream: %s", err)
		return
	}
	logger.Info("{Update Receiver} Listening to ledger updates.")
	for i := 0; i < 2; i++ {
		m, inerr := dstream.Recv()
		logger.Info("{Update Receiver} Got message: ", m, "err:", inerr)
		if inerr != nil {
			errorch <- fmt.Errorf("Failed to receive consensus: %s", inerr)
			return
		}
		b := m.Type.(*ab.DeliverResponse_Block)
		logger.Info("{Update Receiver} Received a ledger update.")
		for i, tx := range b.Block.Data.Data {
			pl := &cb.Payload{}
			e := &cb.Envelope{}
			merr1 := proto.Unmarshal(tx, e)
			merr2 := proto.Unmarshal(e.Payload, pl)
			if merr1 == nil && merr2 == nil {
				logger.Infof("{Update Receiver} %d - %v", i+1, pl.Data)
			}
		}
		resultch <- UPDATE
	}
	logger.Info("{Update Receiver} Exiting...")
}

func broadcastSender(t *testing.T, resultch chan byte, errorch chan error, client ab.AtomicBroadcastClient) {
	logger.Info("{Broadcast Sender} Waiting before sending.")
	<-time.After(5 * time.Second)
	bstream, err := client.Broadcast(context.Background())
	if err != nil {
		errorch <- fmt.Errorf("Failed to get broadcast stream: %s", err)
		return
	}
	bs := []byte{0, 1, 2, 3}
	pl := &cb.Payload{Data: bs}
	mpl, err := proto.Marshal(pl)
	if err != nil {
		panic("Failed to marshal payload.")
	}
	bstream.Send(&cb.Envelope{Payload: mpl})
	logger.Infof("{Broadcast Sender} Broadcast sent: %v", bs)
	logger.Info("{Broadcast Sender} Exiting...")
	resultch <- SEND
}
