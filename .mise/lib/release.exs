# Version and CHANGELOG edits for `mise run release:prepare`. Plain `elixir`,
# no Mix project; any error exits 1 with a message on stderr.
#
#   elixir .mise/lib/release.exs plan DIR CURRENT BUMP
#     Prints the next version. BUMP is patch|minor|major or an explicit
#     version greater than CURRENT. Fails when CHANGELOG.md has nothing under
#     [Unreleased].
#
#   elixir .mise/lib/release.exs apply DIR KIND NEW TAG PREV_TAG DATE
#     Sets the version file for KIND (npm's src/version.ts, Cargo.toml, go's
#     version.go, mix.exs, lib/santati/version.rb, src/Version.php; python's
#     pyproject.toml is `uv version`'s job, so KIND python writes nothing),
#     opens `## [NEW] - DATE` under [Unreleased] in CHANGELOG.md, and points
#     the footer compare links at TAG. PREV_TAG may be "" (first release).
defmodule Release do
  @repo "https://github.com/jamescarr/santati-sdks"

  def main(["plan", dir, current, bump]) do
    ensure_unreleased_entries!(dir)
    IO.puts(next_version(current, bump))
  end

  def main(["apply", dir, kind, new, tag, prev_tag, date]) do
    Version.parse(new) == :error && die("#{new} is not a version")

    write_version!(dir, kind, new)

    changelog = Path.join(dir, "CHANGELOG.md")

    changelog
    |> File.read!()
    |> String.split("\n")
    |> open_section(new, date, changelog)
    |> link_footer(new, tag, prev_tag)
    |> Enum.join("\n")
    |> then(&File.write!(changelog, &1))
  end

  def main(_) do
    die("""
    usage: elixir .mise/lib/release.exs plan DIR CURRENT BUMP
           elixir .mise/lib/release.exs apply DIR KIND NEW TAG PREV_TAG DATE\
    """)
  end

  # The single version source per kind, per `.mise/lib/pkg.sh`'s pkg_version.
  defp write_version!(dir, kind, new) do
    case kind do
      "python" ->
        :ok

      "npm" ->
        rewrite!(
          Path.join(dir, "src/version.ts"),
          ~r/^export const VERSION = '[^']+';$/m,
          "export const VERSION = '#{new}';"
        )

      "cargo" ->
        rewrite!(Path.join(dir, "Cargo.toml"), ~r/^version = "[^"]+"$/m, ~s(version = "#{new}"))

      "go" ->
        rewrite!(Path.join(dir, "version.go"), ~r/^const Version = "[^"]+"$/m, ~s(const Version = "#{new}"))

      "hex" ->
        rewrite!(Path.join(dir, "mix.exs"), ~r/^  @version "[^"]+"$/m, ~s(  @version "#{new}"))

      "gem" ->
        rewrite!(
          Path.join(dir, "lib/santati/version.rb"),
          ~r/^  VERSION = "[^"]+"$/m,
          ~s(  VERSION = "#{new}")
        )

      "composer" ->
        rewrite!(
          Path.join(dir, "src/Version.php"),
          ~r/^    public const VERSION = '[^']+';$/m,
          "    public const VERSION = '#{new}';"
        )

      other ->
        die("unknown kind #{inspect(other)}")
    end
  end

  defp rewrite!(path, regex, replacement) do
    source = File.read!(path)

    case Regex.scan(regex, source) do
      [_] -> File.write!(path, Regex.replace(regex, source, replacement))
      [] -> die("#{path} has no line matching #{inspect(regex)}")
      _ -> die("#{path} has more than one line matching #{inspect(regex)}")
    end
  end

  defp next_version(current, bump) when bump in ["patch", "minor", "major"] do
    %Version{major: ma, minor: mi, patch: pa} = parse!(current)

    next =
      case bump do
        "major" -> %Version{major: ma + 1, minor: 0, patch: 0}
        "minor" -> %Version{major: ma, minor: mi + 1, patch: 0}
        "patch" -> %Version{major: ma, minor: mi, patch: pa + 1}
      end

    Version.to_string(next)
  end

  defp next_version(current, explicit) do
    case Version.parse(explicit) do
      {:ok, next} ->
        Version.compare(next, parse!(current)) == :gt ||
          die("#{explicit} is not greater than the current version #{current}")

        Version.to_string(next)

      :error ->
        die("bump must be patch, minor, major, or a version; got #{inspect(explicit)}")
    end
  end

  defp parse!(v) do
    case Version.parse(v) do
      {:ok, version} -> version
      :error -> die("current version #{inspect(v)} is not SemVer")
    end
  end

  defp ensure_unreleased_entries!(dir) do
    lines = dir |> Path.join("CHANGELOG.md") |> File.read!() |> String.split("\n")

    entries =
      case Enum.find_index(lines, &unreleased?/1) do
        nil ->
          []

        i ->
          lines
          |> Enum.drop(i + 1)
          |> Enum.take_while(&(not String.starts_with?(&1, "## ")))
          |> Enum.reject(&(String.trim(&1) == ""))
      end

    entries == [] && die("#{dir}/CHANGELOG.md has no [Unreleased] entries")
  end

  defp unreleased?(line), do: String.starts_with?(line, "## [Unreleased]")

  defp open_section(lines, new, date, path) do
    case Enum.find_index(lines, &unreleased?/1) do
      nil -> die("#{path} has no ## [Unreleased] heading")
      i -> List.insert_at(lines, i + 1, ["", "## [#{new}] - #{date}"]) |> List.flatten()
    end
  end

  # Keep-a-Changelog reference links: [Unreleased] compares the new tag to
  # HEAD, [NEW] compares the previous tag to the new one (or links the tag
  # itself on a first release). TAG arrives as given, so the Go module's
  # `go/vX.Y.Z` links to the tag that actually exists.
  defp link_footer(lines, new, tag, prev_tag) do
    unreleased = "[Unreleased]: #{@repo}/compare/#{tag}...HEAD"

    release =
      case prev_tag do
        "" -> "[#{new}]: #{@repo}/releases/tag/#{tag}"
        prev -> "[#{new}]: #{@repo}/compare/#{prev}...#{tag}"
      end

    case Enum.find_index(lines, &String.starts_with?(&1, "[Unreleased]: ")) do
      nil ->
        body = lines |> Enum.reverse() |> Enum.drop_while(&(&1 == "")) |> Enum.reverse()
        body ++ ["", unreleased, release, ""]

      i ->
        lines |> List.replace_at(i, unreleased) |> List.insert_at(i + 1, release)
    end
  end

  defp die(message) do
    IO.puts(:stderr, message)
    System.halt(1)
  end
end

Release.main(System.argv())
