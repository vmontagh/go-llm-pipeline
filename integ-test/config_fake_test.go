//go:build !live

package integtest

// This file configures the default (pre-submit) lane. Without the `live` build
// tag the suite selects @fake scenarios, which replay committed cassettes
// offline via pkg/vcrtest. A scenario that also carries @live-local still runs
// here from its cassette; the @live-local tag only adds it to the live lane.
const (
	// defaultGodogTags selects the offline fake lane.
	defaultGodogTags = "@fake"
	// isFakeMode reports that this build replays cassettes rather than
	// recording against a live backend.
	isFakeMode = true
)
