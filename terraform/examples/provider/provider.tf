terraform {
  required_providers {
    santati = {
      source = "jamescarr/santati"
    }
  }
}

# The API key defaults to the SANTATI_API_KEY environment variable and the base
# URL to SANTATI_BASE_URL, then https://api.santati.io.
provider "santati" {}
