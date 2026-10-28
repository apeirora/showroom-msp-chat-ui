#!/usr/bin/env python3
"""Set the operator's packaged default image, rejecting unexpected YAML shape.

This is deliberately a narrow edit to the repository-owned image block. It does
not implement a general YAML parser or change nested portal image settings.
"""
import argparse
from pathlib import Path
import re


VERSION = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
)
IMAGE_KEY = re.compile(r"^(?:image|'image'|\"image\")\s*:")
IMAGE_HEADER = re.compile(r"image:[ ]*(?:#.*)?(?:\r?\n)?")
CHILD = re.compile(r"  (repository|tag|pullPolicy|imagePullSecrets):[ ]+([^\r\n]+)(?:\r?\n)?")
SCALAR = re.compile(
    r"  (repository|tag):[ ]+(?:'([^']+)'|\"([^\"]+)\"|([^\s#]+))([ ]*(?:[ ]#.*)?)(\r?\n)?"
)
REPOSITORY = re.compile(r"ghcr\.io/[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?/chat-ui-controller")


def validate_version(version):
    match = VERSION.fullmatch(version)
    if match is None or len(version) > 128:
        raise ValueError("image version must be SemVer without build metadata or a leading v")
    if match[4] and any(
        item.isdigit() and len(item) > 1 and item.startswith("0")
        for item in match[4].split(".")
    ):
        raise ValueError("numeric prerelease identifiers must not have leading zeros")


def validate_repository(repository):
    if REPOSITORY.fullmatch(repository) is None:
        raise ValueError("image repository must be ghcr.io/<lowercase GitHub owner>/chat-ui-controller")


def release_values(text, version, repository):
    validate_version(version)
    validate_repository(repository)
    lines = text.splitlines(keepends=True)
    roots = [i for i, line in enumerate(lines) if IMAGE_KEY.match(line)]
    if len(roots) != 1 or IMAGE_HEADER.fullmatch(lines[roots[0]]) is None:
        raise ValueError("expected exactly one literal root image block")
    start = roots[0] + 1
    end = len(lines)
    for i in range(start, len(lines)):
        if lines[i].strip() and not lines[i].lstrip().startswith("#") and not lines[i].startswith((" ", "\t")):
            end = i
            break
    fields = {}
    for i in range(start, end):
        if not lines[i].strip() or lines[i].lstrip().startswith("#"):
            continue
        child = CHILD.fullmatch(lines[i])
        if child is None or child[1] in fields:
            raise ValueError("operator image block changed; review the release edit")
        fields[child[1]] = i
    if set(fields) != {"repository", "tag", "pullPolicy", "imagePullSecrets"}:
        raise ValueError("operator image block requires repository, tag, pullPolicy and imagePullSecrets")
    if lines[fields["imagePullSecrets"]].strip() != "imagePullSecrets: []":
        raise ValueError("repository-owned default imagePullSecrets must remain an empty list")
    for field, replacement, validate in [
        ("repository", repository, validate_repository),
        ("tag", version, validate_version),
    ]:
        index = fields[field]
        scalar = SCALAR.fullmatch(lines[index])
        if scalar is None:
            raise ValueError(f"expected one scalar operator image {field}")
        validate(next(value for value in scalar.groups()[1:4] if value is not None))
        lines[index] = f'  {field}: "{replacement}"{scalar[5]}{scalar[6] or ""}'
    return "".join(lines)


def ocm_release_values(text, version, repository):
    """Keep the installer overrides on the same published image, including forks."""
    validate_version(version)
    validate_repository(repository)
    lines = text.splitlines(keepends=True)
    roots = [i for i, line in enumerate(lines) if re.match(r"^(?:operator|'operator'|\"operator\")\s*:", line)]
    if len(roots) != 1 or not re.fullmatch(r"operator:[ ]*(?:#.*)?(?:\r?\n)?", lines[roots[0]]):
        raise ValueError("expected exactly one literal root operator block")
    start, end = roots[0] + 1, len(lines)
    for i in range(start, end):
        if lines[i].strip() and not lines[i].lstrip().startswith("#") and not lines[i].startswith((" ", "\t")):
            end = i
            break
    for field, replacement, validate in (("imageRepository", repository, validate_repository), ("imageTag", version, validate_version)):
        candidates = [i for i in range(start, end) if re.match(r"  (?:" + field + r"|'" + field + r"'|\"" + field + r"\")\s*:", lines[i])]
        if len(candidates) != 1:
            raise ValueError("expected exactly one OCM operator " + field)
        index = candidates[0]
        scalar = re.fullmatch(r"  " + field + r":[ ]+(?:'([^']*)'|\"([^\"]*)\"|([^\s#]+))([ ]*(?:[ ]#.*)?)(\r?\n)?", lines[index])
        if scalar is None:
            raise ValueError("expected scalar OCM operator " + field)
        current = next(value for value in scalar.groups()[:3] if value is not None)
        if current or field != "imageTag":
            validate(current)
        lines[index] = f'  {field}: "{replacement}"{scalar[4]}{scalar[5] or ""}'
    return "".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("values", type=Path)
    parser.add_argument("version")
    parser.add_argument("--repository", required=True)
    parser.add_argument("--ocm-values", type=Path)
    args = parser.parse_args()
    try:
        before = args.values.read_text()
        after = release_values(before, args.version, args.repository)
        ocm_after = ocm_release_values(args.ocm_values.read_text(), args.version, args.repository) if args.ocm_values else None
    except (OSError, ValueError) as error:
        parser.error(str(error))
    args.values.write_text(after)
    if args.ocm_values:
        args.ocm_values.write_text(ocm_after)


if __name__ == "__main__":
    main()
