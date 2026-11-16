#!/usr/bin/env python3
"""Replay generated local speech through the production SpeechTrack, without devices.

The temporary CLI needs Blur's existing signing identity and bundle identifier
because Speech asset reservations are associated with that host identity. This
creates no app bundle, registration, alternate store, permission request or asset
installation. The files and signature exist only in a private temporary folder.
"""
import argparse
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--signing-identity", required=True, help="existing Blur certificate SHA-1; never ad-hoc")
    parser.add_argument("--seconds", type=int, default=480, choices=range(1, 601))
    parser.add_argument("--bundle-id", default="com.hazcod.blur")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    sources = [root / name for name in (
        "MeetingAudioCore.swift", "MeetingAudioLevel.swift", "MeetingAudioSession.swift",
        "MeetingAudioSetup.swift", "MeetingAudioSpeech.swift", "MeetingAudioNative.swift",
        "MeetingAudioABI.swift", "Tests/MeetingAudioReplay.swift")]
    env = os.environ.copy()
    if not env.get("DEVELOPER_DIR") and Path("/Applications/Xcode.app/Contents/Developer").is_dir():
        env["DEVELOPER_DIR"] = "/Applications/Xcode.app/Contents/Developer"
    with tempfile.TemporaryDirectory(prefix="blur-meeting-synthetic-replay-") as temporary:
        directory = Path(temporary)
        audio, executable, info = directory / "generated.aiff", directory / "meeting-replay", directory / "Info.plist"
        info.write_bytes(plistlib.dumps({"CFBundleIdentifier": args.bundle_id,
                                         "CFBundleName": "Blur Synthetic Speech Replay", "CFBundleVersion": "1"}))
        subprocess.run(["/usr/bin/say", "-o", str(audio), "--data-format=BEI16@48000", "-r", "160",
                        "This is a generated speech reliability test. The release review is on Tuesday. "
                        "Please preserve every saved word, keep the source order, and finish the voice note "
                        "without dropping audio. This synthetic sentence repeats to test sustained local transcription."], check=True)
        subprocess.run(["xcrun", "swiftc", "-swift-version", "6", "-warnings-as-errors", "-parse-as-library",
                        "-target", "arm64-apple-macos13.0", *map(str, sources),
                        "-Xlinker", "-sectcreate", "-Xlinker", "__TEXT", "-Xlinker", "__info_plist",
                        "-Xlinker", str(info), "-o", str(executable)], env=env, check=True)
        subprocess.run(["/usr/bin/codesign", "--force", "--sign", args.signing_identity,
                        "--options", "runtime", "--timestamp=none", "--identifier", args.bundle_id, str(executable)], check=True)
        subprocess.run([str(executable), str(audio), str(args.seconds)], env=env, check=True, timeout=args.seconds + 80)


if __name__ == "__main__":
    main()
