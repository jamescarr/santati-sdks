data "santati_event_definitions" "all" {}

output "active_actions" {
  value = [for def in data.santati_event_definitions.all.event_definitions : def.action if def.is_active]
}
