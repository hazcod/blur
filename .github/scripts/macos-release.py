#!/usr/bin/env python3
"""Prepare and verify a complete macOS release bundle; preserve it with ditto."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import struct
import subprocess

BUNDLE_ID = "com.hazcod.blur"
BINARIES = ("Contents/MacOS/blur", "Contents/Helpers/blur-foundation-models", "Contents/Frameworks/libBlurMeetingAudio.dylib", "Contents/Frameworks/libBlurScreenText.dylib")
MANIFEST = "Contents/Resources/BlurRelease.json"


def universal(path):
    with path.open("rb") as source:
        header = source.read(8)
        if len(header) != 8:
            return False
        magic, count = struct.unpack(">II", header)
        if magic not in (0xCAFEBABE, 0xCAFEBABF) or count != 2:
            return False
        size = 20 if magic == 0xCAFEBABE else 32
        records = source.read(size * count)
        if len(records) != size * count:
            return False
        return {struct.unpack_from(">I", records, i * size)[0] for i in range(count)} == {0x0100000C, 0x01000007}


def validate_layout(app):
    app = Path(app)
    if app.name != "Blur.app" or not app.is_dir() or app.is_symlink():
        raise ValueError("release input must be a real Blur.app directory")
    for path in app.rglob("*"):
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError("release bundle contains an unsupported link or special file")
    for relative in BINARIES:
        path = app / relative
        if not path.is_file() or not path.stat().st_mode & 0o111 or not universal(path):
            raise ValueError(f"missing executable universal arm64 + x86_64 binary: {relative}")
    info = plistlib.loads((app / "Contents/Info.plist").read_bytes())
    if info.get("CFBundleIdentifier") != BUNDLE_ID or info.get("CFBundleExecutable") != "blur" or info.get("CFBundlePackageType") != "APPL":
        raise ValueError("unexpected app bundle identity")
    if info.get("LSMinimumSystemVersion") != "13.0":
        raise ValueError("unexpected minimum macOS version")
    if any(not isinstance(info.get(key), str) or not info[key].strip() for key in ("NSMicrophoneUsageDescription", "NSAudioCaptureUsageDescription")):
        raise ValueError("meeting audio permission usage descriptions are missing")
    icon = info.get("CFBundleIconFile", "")
    if Path(icon).name != icon or not icon or not (app / "Contents/Resources" / icon).is_file():
        raise ValueError("app icon is missing")
    return info


def prepare(app, version, build, commit, mode):
    info = validate_layout(app)
    match = re.fullmatch(r"(\d+\.\d+\.\d+)(?:-[0-9A-Za-z.-]+)?", version)
    if not match or not re.fullmatch(r"[1-9][0-9]*", build) or not re.fullmatch(r"[0-9a-f]{40}", commit) or mode not in ("development", "notarized"):
        raise ValueError("invalid release version, build, commit, or signing mode")
    info.update(CFBundleShortVersionString=match[1], CFBundleVersion=build, BlurReleaseVersion=version)
    (app / "Contents/Info.plist").write_bytes(plistlib.dumps(info))
    manifest = {
        "version": version, "build": build, "commit": commit, "bundle_id": BUNDLE_ID,
        "architectures": ["arm64", "x86_64"], "minimum_macos": "13.0", "sandbox": False,
        "signing": "developer-id" if mode == "notarized" else "ad-hoc-development",
        "developer_id_signed": mode == "notarized", "notarized": mode == "notarized",
        "notice": "Developer ID signed and notarized." if mode == "notarized" else "Development build: ad-hoc signed, not Developer ID signed or notarized. Gatekeeper may block this download. Prefer a notarized release for regular use.",
    }
    (app / MANIFEST).write_text(json.dumps(manifest, indent=2) + "\n")


def command(args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    if "--display" in args and "--entitlements" not in args:
        return result.stdout + result.stderr
    return result.stdout


def verify_native(app, mode, run=command):
    run(["codesign", "--verify", "--deep", "--strict", str(app)])
    identities = []
    for path in (app, *(app / relative for relative in BINARIES[1:])):
        run(["codesign", "--verify", "--strict", str(path)])
        # codesign writes display output to stderr, including entitlement XML.
        signature = run(["codesign", "--display", "--verbose=4", str(path)]).decode()
        if not re.search(r"\bflags=[^\n]*\([^)]*\bruntime\b", signature):
            raise ValueError("host and native components require Hardened Runtime")
        if mode == "development":
            if "Signature=adhoc" not in signature:
                raise ValueError("development release must explicitly use ad-hoc signatures")
        elif mode == "notarized":
            authorities = [line for line in signature.splitlines() if line.startswith("Authority=")]
            team = [line for line in signature.splitlines() if line.startswith("TeamIdentifier=")]
            if not authorities or not authorities[0].startswith("Authority=Developer ID Application:") or not team or team == ["TeamIdentifier=not set"]:
                raise ValueError("trusted release is missing its Developer ID identity")
            identities.append((authorities, team))
        else:
            raise ValueError("unknown signing mode")
        try:
            encoded = run(["codesign", "--display", "--entitlements", ":-", str(path)])
            entitlements = plistlib.loads(encoded) if encoded.strip() else {}
        except (ValueError, plistlib.InvalidFileException) as error:
            raise ValueError("invalid signed entitlements") from error
        if not isinstance(entitlements, dict):
            raise ValueError("invalid signed entitlements")
        if any(key in entitlements for key in ("com.apple.security.app-sandbox", "com.apple.security.inherit")):
            raise ValueError("direct-download app and helper must be unsandboxed without sandbox inheritance")
        if any("absolute-path" in key or key == "com.apple.security.get-task-allow" for key in entitlements):
            raise ValueError("development sandbox exceptions are forbidden in releases")
        if any(entitlements.get(key) for key in ("com.apple.security.cs.disable-library-validation", "com.apple.security.cs.allow-dyld-environment-variables")):
            raise ValueError("release library validation must remain enabled")
        if path == app and entitlements.get("com.apple.security.device.audio-input") is not True:
            raise ValueError("meeting microphone requires the host audio-input entitlement")
    if mode == "notarized":
        if any(identity != identities[0] for identity in identities[1:]):
            raise ValueError("app and native components Developer ID identities differ")
        run(["xcrun", "stapler", "validate", str(app)])
        run(["spctl", "--assess", "--type", "execute", "--verbose=2", str(app)])


def archive(app, output, mode, verify=verify_native):
    info = validate_layout(app)
    metadata = json.loads((app / MANIFEST).read_text())
    version = metadata.get("version", "")
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version) or info.get("BlurReleaseVersion") != version:
        raise ValueError("release metadata does not match the app version")
    if metadata.get("notarized") != (mode == "notarized") or metadata.get("signing") != ("developer-id" if mode == "notarized" else "ad-hoc-development"):
        raise ValueError("release metadata does not match signing mode")
    if metadata.get("sandbox") is not False:
        raise ValueError("release metadata must declare an unsandboxed app")
    verify(app, mode)
    output = Path(output).resolve()
    if output == app.resolve() or app.resolve() in output.parents:
        raise ValueError("archive output must be outside the signed app")
    output.mkdir(parents=True, exist_ok=True)
    suffix = "" if mode == "notarized" else "_development"
    destination = output / f"Blur_{version}_macos_universal{suffix}.zip"
    if destination.exists():
        raise ValueError("archive output already exists")
    subprocess.run(["ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", str(app), str(destination)], check=True)
    checksum = hashlib.sha256(destination.read_bytes()).hexdigest()
    destination.with_suffix(".zip.sha256").write_text(f"{checksum}  {destination.name}\n")
    metadata.update(archive=destination.name, archive_sha256=checksum)
    destination.with_suffix(".json").write_text(json.dumps(metadata, indent=2) + "\n")
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "archive"))
    parser.add_argument("--app", required=True, type=Path)
    parser.add_argument("--mode", required=True, choices=("development", "notarized"))
    parser.add_argument("--version")
    parser.add_argument("--build")
    parser.add_argument("--commit")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.operation == "prepare":
        prepare(args.app, args.version or "", args.build or "", args.commit or "", args.mode)
    else:
        if args.output is None:
            parser.error("archive requires --output")
        print(archive(args.app, args.output, args.mode))


if __name__ == "__main__":
    main()
