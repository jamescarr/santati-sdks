defmodule Santati.MixProject do
  use Mix.Project

  @version "0.2.0"

  def project do
    [
      app: :santati,
      version: @version,
      elixir: "~> 1.18",
      elixirc_paths: elixirc_paths(Mix.env()),
      start_permanent: Mix.env() == :prod,
      description: "Official Elixir SDK for the Santati audit-log API",
      source_url: "https://github.com/jamescarr/santati-sdks",
      deps: deps(),
      package: package(),
      # test/chaos_driver.exs is a script for `mise run chaos`, not a test file.
      test_ignore_filters: [
        &String.ends_with?(&1, "test_helper.exs"),
        &String.ends_with?(&1, "chaos_driver.exs")
      ]
    ]
  end

  def application do
    [
      extra_applications: [:logger, :crypto, :ssl, :public_key]
    ]
  end

  defp elixirc_paths(:test), do: ["lib", "test/support"]
  defp elixirc_paths(_), do: ["lib"]

  defp deps do
    [
      {:tesla, "~> 1.14"},
      {:mint, "~> 1.6"},
      {:castore, "~> 1.0"},
      {:redix, "~> 1.5", optional: true},
      {:bandit, "~> 1.6", only: :test},
      {:ex_doc, "~> 0.38", only: :dev, runtime: false}
    ]
  end

  defp package do
    [
      licenses: ["Apache-2.0"],
      links: %{"GitHub" => "https://github.com/jamescarr/santati-sdks"},
      files: ~w(lib mix.exs README.md CHANGELOG.md LICENSE)
    ]
  end
end
