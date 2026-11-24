package protocol

// DeviceFingerprint is the exact json.dumps of DEVICE_INFO from commit 04a0601,
// with separators (",", ":"). It is not encoding/json of a Go map.
const DeviceFingerprint = `{"platform":"android","appName":"Tonkeeper","appVersion":"26.07.1","maxProtocolVersion":2,"features":["SendTransaction",{"name":"SignData","types":["text","binary","cell"]},{"name":"SendTransaction","maxMessages":255}]}`
