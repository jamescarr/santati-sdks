package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/jamescarr/terraform-provider-santati/internal/provider"
)

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(Version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jamescarr/santati",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
