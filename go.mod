module github.com/robeeejay/prusa-tasmota-lights

go 1.26.11

require github.com/prusa3d/prusa-connect/sdk v0.0.0

// The SDK is not published yet, so it builds from a copy of the Prusa Connect
// checkout. .core/SDK_COMMIT records which commit the copy came from.
replace github.com/prusa3d/prusa-connect/sdk => ./.core/sdk
