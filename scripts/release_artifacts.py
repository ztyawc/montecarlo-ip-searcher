#!/usr/bin/env python3
"""Build, inspect, and publish the complete MCIS release without third-party modules."""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import sys
import tarfile
import tempfile
import time
from urllib.parse import quote
import zipfile


ROOT = Path(__file__).resolve().parents[1]
EXTRA_FILES = (
    'LICENSE', 'readme.md', 'ipv4cidr.txt', 'ipv6cidr.txt',
    'private-socks.example.json',
)
TARGETS = tuple((goos, goarch) for goos in ('linux', 'windows', 'darwin')
                for goarch in ('amd64', 'arm64'))
CERTIFICATE_FILE = 'ANDROID-SIGNING-CERT.txt'
CHECKSUM_FILE = 'SHA256SUMS'


def run(args, **kwargs):
    return subprocess.run(args, check=True, cwd=ROOT, **kwargs)


def git(*args):
    return run(['git', *args], capture_output=True, text=True).stdout.strip()


def check_version(tag):
    match = re.fullmatch(r'v(\d+\.\d+\.\d+)(?:-[0-9A-Za-z.-]+)?', tag)
    if not match:
        raise ValueError('An existing version tag such as v0.4.0 is required')
    manifest = (ROOT / 'flutter-app/pubspec.yaml').read_text(encoding='utf-8')
    version = re.search(r'^version:\s*(\d+\.\d+\.\d+)\+(\d+)\s*$', manifest, re.M)
    if not version or match[1] != version[1]:
        raise ValueError('Release tag must match the version name in flutter-app/pubspec.yaml')
    return version[1], version[2]


def cli_name(tag, goos, goarch):
    extension = 'zip' if goos == 'windows' else 'tar.gz'
    return f'mcis-{tag}-{goos}-{goarch}.{extension}'


def apk_name(tag):
    return f'mcis-{tag}-android-arm64-v8a.apk'


def expected_assets(tag):
    return {cli_name(tag, *target) for target in TARGETS} | {apk_name(tag), CERTIFICATE_FILE}


def text_bytes(path):
    # Windows checkout line endings must not change the release documentation.
    return path.read_bytes().replace(b'\r\n', b'\n')


def build_info(tag, goos, goarch):
    return (f'Tag: {tag}\nCommit: {git("rev-parse", "HEAD")}\n'
            f'Target: {goos}/{goarch}\n').encode()


def build_cli(args):
    check_version(args.tag)
    epoch = int(git('show', '-s', '--format=%ct', 'HEAD'))
    binary_name = 'mcis.exe' if args.goos == 'windows' else 'mcis'
    output = args.output.resolve()
    with tempfile.TemporaryDirectory(prefix='mcis-build-') as temporary:
        binary = Path(temporary) / binary_name
        environment = dict(os.environ, GOOS=args.goos, GOARCH=args.goarch, CGO_ENABLED='0')
        run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w',
             '-o', str(binary), './cmd/mcis'], env=environment)
        files = {name: text_bytes(ROOT / name) for name in EXTRA_FILES}
        files[binary_name] = binary.read_bytes()
        files['BUILD-INFO.txt'] = build_info(args.tag, args.goos, args.goarch)
        output.mkdir(parents=True, exist_ok=True)
        archive_path = output / cli_name(args.tag, args.goos, args.goarch)
        if args.goos == 'windows':
            stamp = time.gmtime(max(epoch, 315532800))[:6]
            with zipfile.ZipFile(archive_path, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
                for name, content in sorted(files.items()):
                    entry = zipfile.ZipInfo(name, date_time=stamp)
                    entry.create_system = 3
                    entry.external_attr = (0o100755 if name == binary_name else 0o100644) << 16
                    entry.compress_type = zipfile.ZIP_DEFLATED
                    archive.writestr(entry, content)
        else:
            with archive_path.open('wb') as raw:
                with gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=epoch) as compressed:
                    with tarfile.open(fileobj=compressed, mode='w') as archive:
                        for name, content in sorted(files.items()):
                            entry = tarfile.TarInfo(name)
                            entry.size, entry.mtime = len(content), epoch
                            entry.mode = 0o755 if name == binary_name else 0o644
                            archive.addfile(entry, io.BytesIO(content))
    inspect_cli(archive_path, args.tag, args.goos, args.goarch)
    print(f'CLI package verified: {archive_path.name}')


def inspect_binary(binary, goos, goarch):
    if goos in ('linux', 'android'):
        expected = 62 if goarch == 'amd64' else 183
        valid = (len(binary) > 20 and binary[:6] == b'\x7fELF\x02\x01'
                 and struct.unpack_from('<H', binary, 18)[0] == expected)
    elif goos == 'windows':
        offset = struct.unpack_from('<I', binary, 60)[0] if len(binary) >= 64 else len(binary)
        expected = 0x8664 if goarch == 'amd64' else 0xAA64
        valid = (binary[:2] == b'MZ' and len(binary) >= offset + 6
                 and binary[offset:offset + 4] == b'PE\0\0'
                 and struct.unpack_from('<H', binary, offset + 4)[0] == expected)
    else:
        expected = 0x01000007 if goarch == 'amd64' else 0x0100000C
        valid = (len(binary) >= 8 and binary[:4] == b'\xcf\xfa\xed\xfe'
                 and struct.unpack_from('<I', binary, 4)[0] == expected)
    if not valid:
        raise ValueError(f'Unexpected executable format for {goos}/{goarch}')


def inspect_cli(path, tag, goos, goarch):
    binary_name = 'mcis.exe' if goos == 'windows' else 'mcis'
    expected = {*EXTRA_FILES, binary_name, 'BUILD-INFO.txt'}
    if goos == 'windows':
        with zipfile.ZipFile(path) as archive:
            if archive.testzip() or len(archive.namelist()) != len(expected):
                raise ValueError(f'Invalid ZIP archive: {path.name}')
            files = {name: archive.read(name) for name in archive.namelist()}
    else:
        with tarfile.open(path, 'r:gz') as archive:
            members = archive.getmembers()
            if len(members) != len(expected) or not all(member.isfile() for member in members):
                raise ValueError(f'Invalid tar archive: {path.name}')
            files = {member.name: archive.extractfile(member).read() for member in members}
            executable = next((member for member in members if member.name == binary_name), None)
            if executable is None or not executable.mode & 0o111:
                raise ValueError(f'The CLI is not executable in {path.name}')
    if set(files) != expected:
        raise ValueError(f'Unexpected package contents in {path.name}')
    for name in EXTRA_FILES:
        if files[name] != text_bytes(ROOT / name):
            raise ValueError(f'Bundled {name} differs from the release source')
    if files['BUILD-INFO.txt'] != build_info(tag, goos, goarch):
        raise ValueError(f'CLI provenance does not match the release source: {path.name}')
    inspect_binary(files[binary_name], goos, goarch)
    return binary_name, files[binary_name]


def smoke_cli(args):
    path = args.output / cli_name(args.tag, args.goos, args.goarch)
    name, data = inspect_cli(path, args.tag, args.goos, args.goarch)
    with tempfile.TemporaryDirectory(prefix='mcis-smoke-') as temporary:
        binary = Path(temporary) / name
        binary.write_bytes(data)
        binary.chmod(0o755)
        result = run([str(binary), '--help'], capture_output=True, text=True,
                     encoding='utf-8', errors='replace', timeout=30)
        if 'Usage' not in result.stdout + result.stderr:
            raise ValueError('CLI help output was not recognized')
    print(f'Unpacked {args.goos}/{args.goarch} CLI starts and displays help')


def verify_apk(args):
    version_name, version_code = check_version(args.tag)
    with zipfile.ZipFile(args.output / apk_name(args.tag)) as archive:
        if archive.testzip():
            raise ValueError('APK ZIP integrity check failed')
        names = set(archive.namelist())
        native_abis = {name.split('/')[1] for name in names if name.startswith('lib/') and name.endswith('.so')}
        required = {'lib/arm64-v8a/libmcis.so', 'lib/arm64-v8a/libapp.so',
                    'lib/arm64-v8a/libflutter.so', 'AndroidManifest.xml',
                    'assets/ipv4cidr.txt', 'assets/ipv6cidr.txt'}
        if native_abis != {'arm64-v8a'} or not required <= names:
            raise ValueError('APK must contain the Flutter app, Go core, and CIDR assets for arm64-v8a only')
        inspect_binary(archive.read('lib/arm64-v8a/libmcis.so'), 'android', 'arm64')
        for name in ('ipv4cidr.txt', 'ipv6cidr.txt'):
            if archive.read(f'assets/{name}') != text_bytes(ROOT / name):
                raise ValueError(f'APK contains stale {name}')
    certificate = (args.output / CERTIFICATE_FILE).read_text(encoding='utf-8')
    digest = re.search(r'^Signer #1 certificate SHA-256 digest:\s*([0-9a-fA-F]{64})\s*$', certificate, re.M)
    expected = os.environ.get('ANDROID_SIGNING_CERT_SHA256', '').lower()
    if not re.fullmatch(r'[0-9a-f]{64}', expected) or not digest or digest[1].lower() != expected:
        raise ValueError('APK signing certificate does not match ANDROID_SIGNING_CERT_SHA256')
    if args.badging:
        badging = args.badging.read_text(encoding='utf-8')
        required_values = (f"name='com.ztyawc.mcis'", f"versionCode='{version_code}'",
                           f"versionName='{version_name}'", "targetSdkVersion:'35'")
        minimum_sdk = re.search(r"^(?:minSdkVersion|sdkVersion):'26'\s*$", badging, re.M)
        if not minimum_sdk or not all(value in badging for value in required_values) or 'application-debuggable' in badging:
            raise ValueError('APK identity, version, SDK level, or release mode is incorrect')
    print('Android APK contents, version metadata, and release certificate verified')


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def verify_release(args):
    check_version(args.tag)
    expected = expected_assets(args.tag)
    entries = list(args.output.iterdir())
    actual = {path.name for path in entries}
    if actual - {CHECKSUM_FILE} != expected or not all(path.is_file() and not path.is_symlink() for path in entries):
        raise ValueError(f'Incomplete or unexpected release files: missing={sorted(expected - actual)}, extra={sorted(actual - expected - {CHECKSUM_FILE})}')
    for goos, goarch in TARGETS:
        inspect_cli(args.output / cli_name(args.tag, goos, goarch), args.tag, goos, goarch)
    verify_apk(args)
    checksum_text = ''.join(f'{sha256(args.output / name)}  {name}\n' for name in sorted(expected))
    (args.output / CHECKSUM_FILE).write_text(checksum_text, encoding='utf-8', newline='\n')
    print(f'All {len(expected)} release assets verified; {CHECKSUM_FILE} written')


def gh(*args, check=True):
    result = subprocess.run(['gh', *args], cwd=ROOT, capture_output=True, text=True)
    if check and result.returncode:
        raise RuntimeError(f'GitHub release operation failed: {result.stderr.strip()}')
    return result


def release_details(repository, tag):
    # The REST endpoint for a tag only promises published releases. The CLI also finds drafts.
    result = gh('release', 'view', tag, '--repo', repository,
                '--json', 'isDraft,assets,tagName', check=False)
    if result.returncode:
        if result.stderr.strip() == 'release not found':
            return None
        raise RuntimeError(f'Unable to read release: {result.stderr.strip()}')
    details = json.loads(result.stdout)
    return {'draft': details['isDraft'], 'assets': details['assets']}


def verify_remote_tag(repository, tag):
    endpoint = f'repos/{repository}/git/ref/tags/{quote(tag, safe="")}'
    target = json.loads(gh('api', endpoint).stdout)['object']
    for _ in range(10):
        if target['type'] == 'commit':
            if target['sha'] != git('rev-parse', 'HEAD'):
                raise ValueError('The remote tag moved away from the source commit used for this build')
            return
        if target['type'] != 'tag':
            break
        target = json.loads(gh('api', f'repos/{repository}/git/tags/{target["sha"]}').stdout)['object']
    raise ValueError('The remote release tag does not resolve to a commit')


def upload_release(args):
    # This command is used only by the final Actions job, after every build has passed.
    verify_release(args)
    repository = os.environ.get('GH_REPO') or os.environ.get('GITHUB_REPOSITORY')
    if not repository:
        raise ValueError('GH_REPO or GITHUB_REPOSITORY is required')
    verify_remote_tag(repository, args.tag)
    names = sorted(expected_assets(args.tag) | {CHECKSUM_FILE})
    paths = [args.output.resolve() / name for name in names]
    details = release_details(repository, args.tag)
    if details is None:
        gh('release', 'create', args.tag, '--repo', repository, '--verify-tag', '--draft',
           '--title', f'MCIS {args.tag}', '--generate-notes')
        details = release_details(repository, args.tag)
    if details is None:
        raise RuntimeError('The draft release could not be read after creation')
    draft = details['draft']
    existing = {asset['name'] for asset in details['assets']}
    unexpected = existing - set(names)
    if unexpected:
        raise ValueError(f'The release contains unverified extra assets: {sorted(unexpected)}')
    if not draft and existing.intersection(names):
        raise ValueError('A published release already has matching asset names; historical assets will not be overwritten. Use a new tag.')
    command = ['release', 'upload', args.tag, '--repo', repository, *map(str, paths)]
    if draft:
        command.append('--clobber')
    gh(*command)
    # Verify downloaded bytes before a draft is allowed to become public.
    with tempfile.TemporaryDirectory(prefix='mcis-release-download-') as temporary:
        download = ['release', 'download', args.tag, '--repo', repository, '--dir', temporary]
        for name in names:
            download.extend(['--pattern', name])
        gh(*download)
        for path in paths:
            downloaded = Path(temporary) / path.name
            if not downloaded.is_file() or sha256(downloaded) != sha256(path):
                raise ValueError(f'Release download failed SHA-256 verification: {path.name}')
    verify_remote_tag(repository, args.tag)
    details = release_details(repository, args.tag)
    if details is None or {asset['name'] for asset in details['assets']} != set(names):
        raise ValueError('The remote release asset set changed during verification')
    draft = details['draft']
    if draft and os.environ.get('PUBLISH_RELEASE', 'false').lower() == 'true':
        gh('release', 'edit', args.tag, '--repo', repository, '--draft=false')
        draft = False
    state = 'draft retained for review' if draft else 'published'
    print(f'Release {args.tag}: {len(paths)} assets uploaded and downloaded successfully; {state}')
    if summary := os.environ.get('GITHUB_STEP_SUMMARY'):
        with open(summary, 'a', encoding='utf-8') as output:
            output.write(f'### MCIS {args.tag}\n\nSource: `{git("rev-parse", "HEAD")}`\n\n')
            output.write(f'All {len(paths)} assets passed download SHA-256 verification; {state}.\n\n')
            output.write('\n'.join(f'- `{name}`' for name in names) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('check-version', 'build-cli', 'smoke-cli',
                                            'verify-apk', 'verify-release', 'upload-release'))
    parser.add_argument('--tag', required=True)
    parser.add_argument('--goos', choices=('linux', 'windows', 'darwin'))
    parser.add_argument('--goarch', choices=('amd64', 'arm64'))
    parser.add_argument('--output', type=Path, default=ROOT / 'dist')
    parser.add_argument('--badging', type=Path)
    args = parser.parse_args()
    args.output = args.output.resolve()
    if args.command in ('build-cli', 'smoke-cli') and (args.goos, args.goarch) not in TARGETS:
        parser.error('CLI commands require --goos and --goarch')
    try:
        if args.command == 'check-version':
            name, code = check_version(args.tag)
            print(f'Release tag matches Android {name} (version code {code})')
        else:
            globals()[args.command.replace('-', '_')](args)
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError,
            zipfile.BadZipFile, tarfile.TarError) as error:
        print(f'Release check failed: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
