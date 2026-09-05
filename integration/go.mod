// A module of its own, requiring a RELEASED version of the SDK: the point of
// this suite is the artifact a stranger gets from the proxy, so a replace
// directive pointing at the parent directory would quietly defeat all of it.
//
// scripts/run.sh moves the requirement to the newest published tag for the run
// and puts this file back afterwards.
module github.com/internetdata/sdk-go/integration

go 1.24.0

require github.com/internetdata/sdk-go v1.0.0

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/oapi-codegen/runtime v1.7.0 // indirect
)
