# frozen_string_literal: true

require_relative "lib/santati/version"

Gem::Specification.new do |spec|
  spec.name = "santati"
  spec.version = Santati::VERSION
  spec.authors = ["Santati"]
  spec.email = ["support@santati.io"]

  spec.summary = "Official Ruby SDK for the Santati audit-log API"
  spec.description = "Emit audit events and read them back from the Santati API."
  spec.homepage = "https://github.com/jamescarr/santati-sdks"
  spec.license = "Apache-2.0"
  spec.required_ruby_version = ">= 3.2"

  spec.files = Dir.chdir(__dir__) { Dir["lib/**/*.rb"] } + %w[README.md CHANGELOG.md LICENSE]
  spec.require_paths = ["lib"]

  spec.metadata["source_code_uri"] = "#{spec.homepage}/tree/main/ruby"
  spec.metadata["changelog_uri"] = "#{spec.homepage}/blob/main/ruby/CHANGELOG.md"
  spec.metadata["rubygems_mfa_required"] = "true"

  spec.add_dependency "faraday", ">= 2.0", "< 3"
  spec.add_dependency "faraday-multipart", "~> 1.0"
  spec.add_dependency "marcel", "~> 1.0"
end
