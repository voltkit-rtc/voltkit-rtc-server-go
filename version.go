package voltkit

// Version is the version of this module, as published. It is the one place
// the number lives: the User-Agent header of every request carries it
// (voltkit-rtc-go/<Version>, spec section 8), and the repository's contract
// check compares it with docs/sdk-contract.json.
const Version = "0.1.2"

// userAgent is sent on every request, to the controller and to the rates host
// alike: the SDK's name and version, nothing more. No OS, runtime version or
// hostname: the controller does not need them, and the fewer identifying bits
// in a log line the better.
const userAgent = "voltkit-rtc-go/" + Version
