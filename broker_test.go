package sarama

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func ExampleBroker() {
	broker := NewBroker("localhost:9092")
	err := broker.Open(nil)
	if err != nil {
		panic(err)
	}

	request := MetadataRequest{Topics: []string{"myTopic"}}
	response, err := broker.GetMetadata(&request)
	if err != nil {
		_ = broker.Close()
		panic(err)
	}

	fmt.Println("There are", len(response.Topics), "topics active in the cluster.")

	if err = broker.Close(); err != nil {
		panic(err)
	}
}

type mockEncoder struct {
	bytes []byte
}

func (m mockEncoder) encode(pe packetEncoder) error {
	return pe.putRawBytes(m.bytes)
}

type brokerMetrics struct {
	bytesRead    int
	bytesWritten int
}

func TestBrokerAccessors(t *testing.T) {
	broker := NewBroker("abc:123")

	if broker.ID() != -1 {
		t.Error("New broker didn't have an ID of -1.")
	}

	if broker.Addr() != "abc:123" {
		t.Error("New broker didn't have the correct address")
	}

	if broker.Rack() != "" {
		t.Error("New broker didn't have an unknown rack.")
	}

	broker.id = 34
	if broker.ID() != 34 {
		t.Error("Manually setting broker ID did not take effect.")
	}

	rack := "dc1"
	broker.rack = &rack
	if broker.Rack() != rack {
		t.Error("Manually setting broker rack did not take effect.")
	}
}

func TestSimpleBrokerCommunication(t *testing.T) {
	for _, tt := range brokerTestTable {
		Logger.Printf("Testing broker communication for %s", tt.name)
		mb := NewMockBroker(t, 0)
		mb.Returns(&mockEncoder{tt.response})
		pendingNotify := make(chan brokerMetrics)
		// Register a callback to be notified about successful requests
		mb.SetNotifier(func(bytesRead, bytesWritten int) {
			pendingNotify <- brokerMetrics{bytesRead, bytesWritten}
		})
		broker := NewBroker(mb.Addr())
		// Set the broker id in order to validate local broker metrics
		broker.id = 0
		conf := NewConfig()
		conf.Version = tt.version
		err := broker.Open(conf)
		if err != nil {
			t.Fatal(err)
		}
		tt.runner(t, broker)
		// Wait up to 500 ms for the remote broker to process the request and
		// notify us about the metrics
		timeout := 500 * time.Millisecond
		select {
		case mockBrokerMetrics := <-pendingNotify:
			validateBrokerMetrics(t, broker, mockBrokerMetrics)
		case <-time.After(timeout):
			t.Errorf("No request received for: %s after waiting for %v", tt.name, timeout)
		}
		mb.Close()
		err = broker.Close()
		if err != nil {
			t.Error(err)
		}
	}

}

func TestReceiveSASLOAuthBearerServerResponse(t *testing.T) {

	testTable := []struct {
		name string
		buf  []byte
		err  error
	}{
		{"OK server response",
			[]byte{
				0, 0, 0, 14,
				0, 0, 0, 0,
				0, 0,
				255, 255, // no error message
				0, 0, 0, 2, 'o', 'k',
			},
			nil},
		{"SASL authentication failure response",
			[]byte{
				0, 0, 0, 19,
				0, 0, 0, 0,
				0, 58,
				0, 3, 'e', 'r', 'r',
				0, 0, 0, 4, 'f', 'a', 'i', 'l',
			},
			ErrSASLAuthenticationFailed},
		{"Truncated header",
			[]byte{
				0, 0, 0, 12,
			},
			io.ErrUnexpectedEOF},
		{"Truncated response message",
			[]byte{
				0, 0, 0, 12,
				0, 0, 0, 0,
				0, 0,
			},
			io.ErrUnexpectedEOF},
	}

	for i, test := range testTable {

		in, out := net.Pipe()

		defer func() {
			if err := out.Close(); err != nil {
				t.Error(err)
			}
		}()

		b := &Broker{conn: out}

		go func() {
			defer func() {
				if err := in.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := in.Write(test.buf); err != nil {
				t.Error(err)
			}
		}()

		bytesRead, err := b.receiveSASLOAuthBearerServerResponse(0)

		if len(test.buf) != bytesRead {
			t.Errorf("[%d]:[%s] Expected %d bytes read, got %d\n", i, test.name, len(test.buf), bytesRead)
		}

		if test.err != err {
			t.Errorf("[%d]:[%s] Expected %s error, got %s\n", i, test.name, test.err, err)
		}
	}
}

// We're not testing encoding/decoding here, so most of the requests/responses will be empty for simplicity's sake
var brokerTestTable = []struct {
	version  KafkaVersion
	name     string
	response []byte
	runner   func(*testing.T, *Broker)
}{
	{V0_10_0_0,
		"MetadataRequest",
		[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := MetadataRequest{}
			response, err := broker.GetMetadata(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("Metadata request got no response!")
			}
		}},

	{V0_10_0_0,
		"ConsumerMetadataRequest",
		[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 't', 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := ConsumerMetadataRequest{}
			response, err := broker.GetConsumerMetadata(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("Consumer Metadata request got no response!")
			}
		}},

	{V0_10_0_0,
		"ProduceRequest (NoResponse)",
		[]byte{},
		func(t *testing.T, broker *Broker) {
			request := ProduceRequest{}
			request.RequiredAcks = NoResponse
			response, err := broker.Produce(&request)
			if err != nil {
				t.Error(err)
			}
			if response != nil {
				t.Error("Produce request with NoResponse got a response!")
			}
		}},

	{V0_10_0_0,
		"ProduceRequest (WaitForLocal)",
		[]byte{0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := ProduceRequest{}
			request.RequiredAcks = WaitForLocal
			response, err := broker.Produce(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("Produce request without NoResponse got no response!")
			}
		}},

	{V0_10_0_0,
		"FetchRequest",
		[]byte{0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := FetchRequest{}
			response, err := broker.Fetch(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("Fetch request got no response!")
			}
		}},

	{V0_10_0_0,
		"OffsetFetchRequest",
		[]byte{0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := OffsetFetchRequest{}
			response, err := broker.FetchOffset(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("OffsetFetch request got no response!")
			}
		}},

	{V0_10_0_0,
		"OffsetCommitRequest",
		[]byte{0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := OffsetCommitRequest{}
			response, err := broker.CommitOffset(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("OffsetCommit request got no response!")
			}
		}},

	{V0_10_0_0,
		"OffsetRequest",
		[]byte{0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := OffsetRequest{}
			response, err := broker.GetAvailableOffsets(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("Offset request got no response!")
			}
		}},

	{V0_10_0_0,
		"JoinGroupRequest",
		[]byte{0x00, 0x17, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := JoinGroupRequest{}
			response, err := broker.JoinGroup(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("JoinGroup request got no response!")
			}
		}},

	{V0_10_0_0,
		"SyncGroupRequest",
		[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := SyncGroupRequest{}
			response, err := broker.SyncGroup(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("SyncGroup request got no response!")
			}
		}},

	{V0_10_0_0,
		"LeaveGroupRequest",
		[]byte{0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := LeaveGroupRequest{}
			response, err := broker.LeaveGroup(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("LeaveGroup request got no response!")
			}
		}},

	{V0_10_0_0,
		"HeartbeatRequest",
		[]byte{0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := HeartbeatRequest{}
			response, err := broker.Heartbeat(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("Heartbeat request got no response!")
			}
		}},

	{V0_10_0_0,
		"ListGroupsRequest",
		[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := ListGroupsRequest{}
			response, err := broker.ListGroups(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("ListGroups request got no response!")
			}
		}},

	{V0_10_0_0,
		"DescribeGroupsRequest",
		[]byte{0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := DescribeGroupsRequest{}
			response, err := broker.DescribeGroups(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("DescribeGroups request got no response!")
			}
		}},

	{V0_10_0_0,
		"ApiVersionsRequest",
		[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := ApiVersionsRequest{}
			response, err := broker.ApiVersions(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("ApiVersions request got no response!")
			}
		}},

	{V1_1_0_0,
		"DeleteGroupsRequest",
		[]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		func(t *testing.T, broker *Broker) {
			request := DeleteGroupsRequest{}
			response, err := broker.DeleteGroups(&request)
			if err != nil {
				t.Error(err)
			}
			if response == nil {
				t.Error("DeleteGroups request got no response!")
			}
		}},
}

func validateBrokerMetrics(t *testing.T, broker *Broker, mockBrokerMetrics brokerMetrics) {
	metricValidators := newMetricValidators()
	mockBrokerBytesRead := mockBrokerMetrics.bytesRead
	mockBrokerBytesWritten := mockBrokerMetrics.bytesWritten

	// Check that the number of bytes sent corresponds to what the mock broker received
	metricValidators.registerForAllBrokers(broker, countMeterValidator("incoming-byte-rate", mockBrokerBytesWritten))
	if mockBrokerBytesWritten == 0 {
		// This a ProduceRequest with NoResponse
		metricValidators.registerForAllBrokers(broker, countMeterValidator("response-rate", 0))
		metricValidators.registerForAllBrokers(broker, countHistogramValidator("response-size", 0))
		metricValidators.registerForAllBrokers(broker, minMaxHistogramValidator("response-size", 0, 0))
	} else {
		metricValidators.registerForAllBrokers(broker, countMeterValidator("response-rate", 1))
		metricValidators.registerForAllBrokers(broker, countHistogramValidator("response-size", 1))
		metricValidators.registerForAllBrokers(broker, minMaxHistogramValidator("response-size", mockBrokerBytesWritten, mockBrokerBytesWritten))
	}

	// Check that the number of bytes received corresponds to what the mock broker sent
	metricValidators.registerForAllBrokers(broker, countMeterValidator("outgoing-byte-rate", mockBrokerBytesRead))
	metricValidators.registerForAllBrokers(broker, countMeterValidator("request-rate", 1))
	metricValidators.registerForAllBrokers(broker, countHistogramValidator("request-size", 1))
	metricValidators.registerForAllBrokers(broker, minMaxHistogramValidator("request-size", mockBrokerBytesRead, mockBrokerBytesRead))

	// Run the validators
	metricValidators.run(t, broker.conf.MetricRegistry)
}
