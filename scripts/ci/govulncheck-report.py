#!/usr/bin/env python3
"""Turn govulncheck's JSON stream into a verdict and a readable summary.

Reads the stream on stdin, writes the summary on stdout, and says in the exit
code whether anything reachable was found:

    0   nothing reachable
    1   at least one vulnerability is reachable from this code
    2   the input could not be read

## Why the JSON output rather than the plain one

With `-format json`, govulncheck exits 0 whatever it finds. That is the point:
a non-zero exit then means the tool itself failed, and the two cases stop being
indistinguishable. Deciding what counts as a failure belongs here, where the
reasoning can be written down, and not in an exit code with two meanings.

## What counts

govulncheck reports at three levels of certainty, and the difference matters
more than the count does:

    called      a vulnerable function is reachable from this code
    imported    the vulnerable package is imported, the function is not reached
    module      the module is in the build, nothing more is known

Only `called` fails a build. That is the whole reason for preferring
govulncheck to a scanner that works off the dependency list: a finding it
reports is one somebody should read. Failing on `imported` would hand back the
noise that was the reason to switch.

The others are printed, because "not reachable today" is a statement about the
current call graph and a refactor can change it.

## The Go version

Almost every finding in a small project is in the standard library, and which
ones appear depends entirely on the toolchain doing the scanning. A workstation
one patch release behind reports vulnerabilities that CI does not have. So the
version is printed next to the verdict - otherwise the next person spends an
afternoon chasing a finding that only exists on their machine.
"""

import collections
import json
import os
import sys


def parse(stream):
    """govulncheck writes a sequence of JSON objects, not an array."""
    text = stream.read()
    decoder = json.JSONDecoder()
    objects = []
    i = 0
    while i < len(text):
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            break
        obj, i = decoder.raw_decode(text, i)
        objects.append(obj)
    return objects


def level_of(finding):
    """How certain govulncheck is that this one matters.

    The first trace entry is the most specific thing it knows. A `function`
    there means reachable; a `package` means imported but not reached; neither
    means the module is present and nothing more.
    """
    first = finding["trace"][0]
    if "function" in first:
        return "called"
    if "package" in first:
        return "imported"
    return "module"


def main() -> int:
    try:
        objects = parse(sys.stdin)
    except (json.JSONDecodeError, UnicodeDecodeError) as err:
        print(f"::error::Could not read the govulncheck output: {err}")
        return 2

    if not objects:
        print("::error::govulncheck produced no output at all.")
        return 2

    config = {}
    advisories = {}
    findings = []

    for obj in objects:
        if "config" in obj:
            config = obj["config"]
        elif "finding" in obj:
            findings.append(obj["finding"])
        elif "osv" in obj and isinstance(obj["osv"], dict):
            advisories[obj["osv"]["id"]] = obj["osv"]

    by_level = collections.defaultdict(lambda: collections.defaultdict(set))
    for finding in findings:
        level = level_of(finding)
        first = finding["trace"][0]
        if level == "called":
            where = "{}.{}".format(first.get("package", first.get("module", "?")),
                                   first["function"])
        else:
            where = first.get("package", first.get("module", "?"))
        by_level[level][finding["osv"]].add(where)

    # govulncheck emits one finding per level, so a reachable vulnerability
    # also appears as imported and as present in the build. Listing it three
    # times turns a short report into an unreadable one - each is shown at the
    # highest level it reached and nowhere else.
    for lower, higher in (("imported", ("called",)),
                          ("module", ("called", "imported"))):
        for level in higher:
            for osv_id in by_level[level]:
                by_level[lower].pop(osv_id, None)

    scanner = config.get("scanner_version", "unknown")
    mode = config.get("scan_mode", config.get("scan_level", "source"))

    # In binary mode govulncheck reports no go_version, and the scanner's own
    # is not the interesting one anyway: what decides which standard library
    # findings apply is the toolchain that built the binary. govulncheck.sh
    # reads that with `go version -m` and passes it in.
    go = config.get("go_version") or os.environ.get("SCANNED_GO_VERSION") or "unknown"

    print(f"govulncheck {scanner}, Go {go}, mode {mode}")
    print()

    def summary_of(osv_id):
        advisory = advisories.get(osv_id, {})
        return advisory.get("summary") or advisory.get("details", "").split("\n")[0] or "?"

    def fixed_in(osv_id):
        for finding in findings:
            if finding["osv"] == osv_id and finding.get("fixed_version"):
                return finding["fixed_version"]
        return None

    called = by_level["called"]

    for level, heading in (
        ("called", "Reachable"),
        ("imported", "Imported, not reached"),
        ("module", "In the build, nothing more known"),
    ):
        group = by_level[level]
        if not group:
            continue
        print(f"## {heading}: {len(group)}")
        for osv_id in sorted(group):
            fix = fixed_in(osv_id)
            fix_note = f", fixed in {fix}" if fix else ""
            print(f"  {osv_id}{fix_note}")
            print(f"      {summary_of(osv_id)}")
            if level == "called":
                for where in sorted(group[osv_id]):
                    print(f"      -> {where}")
        print()

    if not findings:
        print("Nothing found.")

    if called:
        built_with = "built with" if mode == "binary" else "scanned with"
        print(f"::error::{len(called)} reachable vulnerabilities, {built_with} Go {go}. "
              f"Most findings in a small project are in the standard library, so "
              f"check that version before anything else.")
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
