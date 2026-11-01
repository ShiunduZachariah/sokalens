package main

import "testing"

func TestParseQueuesDefaultsToProviderQueues(t *testing.T) {
	queues := parseQueues("")

	want := []string{"provider-reference", "provider-fixtures", "provider-odds"}
	if len(queues) != len(want) {
		t.Fatalf("queues = %#v, want %#v", queues, want)
	}
	for index := range want {
		if queues[index] != want[index] {
			t.Fatalf("queues = %#v, want %#v", queues, want)
		}
	}
}

func TestParseQueuesTrimsAndDropsEmptyValues(t *testing.T) {
	queues := parseQueues(" provider-fixtures, ,provider-odds ")

	want := []string{"provider-fixtures", "provider-odds"}
	if len(queues) != len(want) {
		t.Fatalf("queues = %#v, want %#v", queues, want)
	}
	for index := range want {
		if queues[index] != want[index] {
			t.Fatalf("queues = %#v, want %#v", queues, want)
		}
	}
}
