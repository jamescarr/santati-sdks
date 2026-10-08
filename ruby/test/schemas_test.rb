# frozen_string_literal: true

require_relative "test_helper"

# Local validation of the schemas resource, without a server.
class SchemasTest < Minitest::Test
  def test_a_nil_version_is_a_validation_error_before_any_request
    client = Santati::Client.new(api_key: "sat_sk_x", base_url: "http://127.0.0.1:1")

    error = assert_raises(Santati::ValidationError) { client.schemas.get_version("invoice.voided", nil) }

    assert_nil error.status
  end
end
