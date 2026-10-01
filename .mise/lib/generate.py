#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = ["pyyaml==6.0.3"]
# ///
"""Prune the vendored control-plane spec into a generation profile and run
openapi-generator over it, one core per language.

    .mise/lib/generate.py [--check] [CORE ...]

The profile (spec/santati-v0.sdk.yaml) is what the generators see; every
rewrite below exists because a trial generation of this spec failed without it
(the reason is in the function that applies it). Generated trees are copied to
the paths generator/config.json lists and nowhere else.

--check regenerates into a temporary root and diffs it against the repo: the
`mise run check:drift` gate.
"""

from __future__ import annotations

import argparse
import difflib
import filecmp
import glob
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
CONFIG = json.loads((ROOT / "generator" / "config.json").read_text())
PROFILE = ROOT / "spec" / "santati-v0.sdk.yaml"
SPEC = ROOT / "spec" / "santati-v0.yaml"

BATCH_ITEM_RESULT = {
    "type": "object",
    "description": (
        "One batch result item: accepted or duplicate items carry id, "
        "rejected items carry error."
    ),
    "required": ["index", "status"],
    "properties": {
        "index": {"type": "integer"},
        "status": {"type": "string", "enum": ["accepted", "duplicate", "rejected"]},
        "id": {"type": "string"},
        "error": {"$ref": "#/components/schemas/ErrorBody"},
    },
}

# Timestamps stay the exact RFC 3339 strings the server sends; Elixir's
# generator referenced a non-existent `SantatiCore.Model.Uri` for `format: uri`.
DROPPED_FORMATS = {"date-time", "date", "uri"}


class Failure(SystemExit):
    def __init__(self, message: str) -> None:
        super().__init__(f"generate.py: {message}")


def die(message: str) -> None:
    raise Failure(message)


def operations_of(spec: dict) -> list[dict]:
    return [
        operation
        for path in spec.get("paths", {}).values()
        for operation in path.values()
        if isinstance(operation, dict) and "operationId" in operation
    ]


def keep_operations(spec: dict, operations: list[str]) -> None:
    """Only the operations the SDKs expose: every other path (and its schemas)
    would otherwise drag the whole control-plane API into every core."""
    wanted = set(operations)
    found = {operation["operationId"] for operation in operations_of(spec)}
    missing = sorted(wanted - found)
    if missing:
        die(f"operations not in spec: {', '.join(missing)}")
    for path, item in list(spec.get("paths", {}).items()):
        kept = {
            method: operation
            for method, operation in item.items()
            if isinstance(operation, dict) and operation.get("operationId") in wanted
        }
        if kept:
            spec["paths"][path] = kept
        else:
            del spec["paths"][path]


def trim_operations(spec: dict) -> None:
    """`metadata.<key>` parameters have templated names no generator can
    express; error-body schemas come from the facades, which read the raw body
    (the control plane's own client generation drops them the same way)."""
    for operation in operations_of(spec):
        parameters = [
            parameter
            for parameter in operation.get("parameters", [])
            if "<" not in parameter.get("name", "")
        ]
        if parameters:
            operation["parameters"] = parameters
        else:
            operation.pop("parameters", None)
        for status, response in operation.get("responses", {}).items():
            if not str(status).startswith("2"):
                response.pop("content", None)


def flatten_batch_item_result(spec: dict) -> None:
    """PHP flattened the oneOf (with the rejected-only enum) and threw on every
    accepted item; a flat object with all three fields generates correctly in
    every language."""
    schemas = spec["components"]["schemas"]
    item = schemas.get("EventBatchItemResult")
    expected = {
        "oneOf": [
            {"$ref": "#/components/schemas/EventBatchAcceptedItem"},
            {"$ref": "#/components/schemas/EventBatchRejectedItem"},
        ]
    }
    if item != expected:
        die(
            "components.schemas.EventBatchItemResult is not exactly the expected oneOf; "
            "update .mise/lib/generate.py"
        )
    schemas["EventBatchItemResult"] = json.loads(json.dumps(BATCH_ITEM_RESULT))


def refs_in(node) -> list[str]:
    if isinstance(node, dict):
        if isinstance(node.get("$ref"), str) and node["$ref"].startswith("#/components/"):
            return [node["$ref"]]
        return [ref for value in node.values() for ref in refs_in(value)]
    if isinstance(node, list):
        return [ref for item in node for ref in refs_in(item)]
    return []


def prune_schemas(spec: dict) -> None:
    """Schemas the kept paths cannot reach are dead weight — and TypeScript's
    generator emits an uncompilable `FieldErrors` map for one of them.

    Only `components.schemas` is pruned. `securitySchemes` is never `$ref`'d: each
    operation names its scheme in `security:`, so a prune over every section
    drops the scheme and leaves the operations requiring one that no longer
    exists — and the cores with no api-key support at all."""
    schemas = spec["components"]["schemas"]
    reachable: set[str] = set()
    pending = refs_in(spec.get("paths", {}))
    while pending:
        pointer = pending.pop()
        if pointer in reachable:
            continue
        reachable.add(pointer)
        pending.extend(refs_in(resolve(spec, pointer)))
    spec["components"]["schemas"] = {
        name: value for name, value in schemas.items() if f"#/components/schemas/{name}" in reachable
    }


def resolve(spec: dict, pointer: str):
    node = spec
    for part in pointer.lstrip("#/").split("/"):
        node = node[part]
    return node


def strip_string_formats(node) -> None:
    if isinstance(node, dict):
        if node.get("type") == "string" and node.get("format") in DROPPED_FORMATS:
            del node["format"]
        for value in node.values():
            strip_string_formats(value)
    elif isinstance(node, list):
        for item in node:
            strip_string_formats(item)


def strip_length_bounds(node) -> None:
    """`minLength`/`maxLength` are documentation: the control plane's serializers
    say so, and ingest clips an over-long value or defaults an empty one instead
    of rejecting it. Cores that enforce the bounds (Python, Ruby, PHP) would
    reject input the server accepts — and fail to decode a response that
    exceeds one — while the others would not, so the server stays the only
    validator."""
    if isinstance(node, dict):
        if node.get("type") == "string":
            node.pop("minLength", None)
            node.pop("maxLength", None)
        for value in node.values():
            strip_length_bounds(value)
    elif isinstance(node, list):
        for item in node:
            strip_length_bounds(item)


def strip_read_write_only(node) -> None:
    """Python's `to_dict()` silently dropped every read-only AuditEvent field."""
    if isinstance(node, dict):
        node.pop("readOnly", None)
        node.pop("writeOnly", None)
        for value in node.values():
            strip_read_write_only(value)
    elif isinstance(node, list):
        for item in node:
            strip_read_write_only(item)


def prepare() -> dict:
    if not SPEC.exists():
        die(f"{SPEC} does not exist; run `mise run spec:sync` first")
    spec = yaml.safe_load(SPEC.read_text())
    keep_operations(spec, CONFIG["operations"])
    trim_operations(spec)
    flatten_batch_item_result(spec)
    prune_schemas(spec)
    strip_string_formats(spec)
    strip_length_bounds(spec)
    strip_read_write_only(spec)
    return spec


def write_profile(spec: dict, root: Path) -> None:
    path = root / "spec" / "santati-v0.sdk.yaml"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(yaml.safe_dump(spec, sort_keys=False, allow_unicode=True, width=120))


def generate_cores(cores: list[str], out_root: Path, profile_root: Path) -> None:
    if shutil.which("docker") is None:
        die("docker is required")
    for core in cores:
        settings = CONFIG["cores"][core]
        with tempfile.TemporaryDirectory() as stage_dir:
            stage = Path(stage_dir)
            shutil.copyfile(profile_root / "spec" / "santati-v0.sdk.yaml", stage / "spec.yaml")
            command = [
                "docker",
                "run",
                "--rm",
                "-v",
                f"{stage}:/stage",
                "--user",
                f"{os.getuid()}:{os.getgid()}",
                CONFIG["image"],
                "generate",
                "-i",
                "/stage/spec.yaml",
                "-g",
                settings["generator"],
                "-o",
                f"/stage/{core}",
                "--global-property",
                "apiDocs=false,modelDocs=false,apiTests=false,modelTests=false",
                "--additional-properties",
                ",".join(f"{key}={value}" for key, value in settings["properties"].items()),
            ]
            if settings.get("nameMappings"):
                command += [
                    "--name-mappings",
                    ",".join(f"{key}={value}" for key, value in settings["nameMappings"].items()),
                ]
            result = subprocess.run(command, capture_output=True, text=True)
            if result.returncode != 0:
                sys.stderr.write(result.stderr or result.stdout)
                die(f"openapi-generator failed for {core} (exit {result.returncode})")
            copy_outputs(core, stage / core, out_root)


def copy_outputs(core: str, generated: Path, out_root: Path) -> None:
    for output in CONFIG["cores"][core]["outputs"]:
        target = out_root / output["to"]
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()
        source = generated / output["from"]
        if "*" in output["from"]:
            target.mkdir(parents=True, exist_ok=True)
            for match in sorted(glob.glob(str(source))):
                shutil.copyfile(match, target / Path(match).name)
        elif source.is_dir():
            shutil.copytree(source, target)
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)


def repo_files(path: Path) -> dict[Path, Path]:
    """The repo's side of a comparison: the files under `path` that git does not
    ignore. Build output such as a test run's `__pycache__` is in .gitignore and
    never counts as drift, while a stray untracked file in a generated tree
    still does."""
    result = subprocess.run(
        ["git", "ls-files", "-co", "--exclude-standard", "-z", "--", str(path.relative_to(ROOT))],
        cwd=ROOT,
        capture_output=True,
        check=False,
    )
    if result.returncode != 0:
        die(f"git ls-files failed: {result.stderr.decode().strip()}")
    files = (ROOT / name for name in result.stdout.decode().split("\0") if name)
    return {
        (Path(path.name) if path.is_file() else file.relative_to(path)): file for file in files if file.exists()
    }


def generated_files(path: Path) -> dict[Path, Path]:
    """The fresh tree's side: everything in it was just generated."""
    if path.is_file():
        return {Path(path.name): path}
    if not path.exists():
        return {}
    return {file.relative_to(path): file for file in sorted(path.rglob("*")) if file.is_file()}


def diff_paths(repo: Path, generated: Path) -> str:
    report = []
    repo_side = repo_files(repo)
    generated_side = generated_files(generated)
    for name in sorted(set(repo_side) | set(generated_side)):
        if name not in generated_side:
            report.append(f"only in repo: {repo_side[name]}\n")
        elif name not in repo_side:
            report.append(f"only in generated: {generated_side[name]}\n")
        else:
            report.extend(diff_files(repo_side[name], generated_side[name]))
    return "".join(report)


def diff_files(left: Path, right: Path) -> list[str]:
    if filecmp.cmp(left, right, shallow=False):
        return []
    name = left.name
    left_lines = left.read_text(errors="replace").splitlines(keepends=True)
    right_lines = right.read_text(errors="replace").splitlines(keepends=True)
    return list(difflib.unified_diff(left_lines, right_lines, f"a/{name}", f"b/{name}"))


def main() -> int:
    parser = argparse.ArgumentParser(prog="generate.py")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("cores", nargs="*", default=[])
    args = parser.parse_args()

    cores = args.cores or list(CONFIG["cores"])
    unknown = [core for core in cores if core not in CONFIG["cores"]]
    if unknown:
        die(f"unknown cores: {', '.join(unknown)} (known: {', '.join(CONFIG['cores'])})")

    spec = prepare()
    if not args.check:
        write_profile(spec, ROOT)
        generate_cores(cores, ROOT, ROOT)
        print(f"generated {', '.join(cores)}")
        return 0

    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        write_profile(spec, root)
        generate_cores(cores, root, root)
        stale = []
        for path in ["spec/santati-v0.sdk.yaml"] + [
            output["to"] for core in cores for output in CONFIG["cores"][core]["outputs"]
        ]:
            report = diff_paths(ROOT / path, root / path)
            if report:
                stale.append(path)
                print(f"--- {path}")
                print(report)
        if stale:
            sys.stderr.write("generated code is stale: run mise run generate\n")
            return 1
    print("generated code is current")
    return 0


if __name__ == "__main__":
    sys.exit(main())
