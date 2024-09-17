package foo

import rego.v1

default allow := false 

token := opa.runtime()["token"]

allow if {
	input.foo == "bar"	
}
