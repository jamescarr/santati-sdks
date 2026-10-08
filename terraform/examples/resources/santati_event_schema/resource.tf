resource "santati_event_schema" "invoice_voided" {
  action = "invoice.voided"
  schema = jsonencode({
    "$schema" = "https://json-schema.org/draft/2020-12/schema"
    type      = "object"
    properties = {
      metadata = {
        type = "object"
        properties = {
          invoice_number = { type = "string" }
          void_reason    = { type = "string" }
        }
        required             = ["invoice_number"]
        additionalProperties = { type = "string" }
      }
    }
  })
}
