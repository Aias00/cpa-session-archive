module github.com/yetone/magpie/plugins/sessionarchive

go 1.26.8

require (
 cpa-session-archive v0.0.0
 github.com/yetone/magpie v0.0.0
)

// The archive currently uses a local module identity; keep that limitation
// explicit rather than pretending this SDK is remotely go-gettable.
replace cpa-session-archive => ../..
