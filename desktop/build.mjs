import {spawn, spawnSync} from 'node:child_process';
import {cp, mkdir, mkdtemp, readFile, rename, rm, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {packager} from '@electron/packager';

const desktop = fileURLToPath(new URL('./', import.meta.url));
const root = path.resolve(desktop, '..');
const dir = path.resolve(root, process.env.YTDL_BUILD_OUT || 'dist');
const info = JSON.parse(await readFile(path.join(desktop, 'package.json'), 'utf8'));
const platforms = ['darwin', 'win32', 'linux'];
const allTargets = platforms.flatMap(platform => ['arm64', 'x64'].map(arch => [platform, arch]));
const selected = process.argv.find(arg => arg.startsWith('--target='))?.slice(9);
const targets = selected ? allTargets.filter(target => target.join('-') === selected)
  : process.argv.includes('--all') ? allTargets
  : [[process.platform, process.arch]];
if (!targets.length) throw new Error('Unknown desktop target: ' + selected);
const dev = process.argv.includes('--dev');

function go(args, env) {
  const result = spawnSync('go', args, {cwd: root, env, stdio: 'inherit'});
  if (result.status !== 0) throw result.error || new Error('go ' + args[0] + ' failed');
}

function buildCore(platform, arch, location) {
  const binary = path.join(location, platform === 'win32' ? 'downloader-core.exe' : 'downloader-core');
  go(['build', '-trimpath', '-ldflags=-s -w', '-o', binary, '.'], {
    ...process.env,
    GOOS: platform === 'win32' ? 'windows' : platform,
    GOARCH: arch === 'x64' ? 'amd64' : arch,
    CGO_ENABLED: '0',
  });
  return binary;
}

async function build(platform, arch) {
  const stage = await mkdtemp(path.join(tmpdir(), 'ytd-desktop-'));
  try {
    const source = path.join(stage, 'source');
    await mkdir(path.join(source, 'assets'), {recursive: true});
    for (const file of ['main.cjs', 'preload.cjs', 'sniffer.cjs', 'sniffer-preload.cjs', 'media-candidates.cjs', 'assets/icon.png']) {
      await cp(path.join(desktop, file), path.join(source, file));
    }
    await writeFile(path.join(source, 'package.json'), JSON.stringify({
      name: info.name, productName: info.productName, version: info.version, main: info.main,
    }));
    const core = buildCore(platform, arch, stage);
    const [bundle] = await packager({
      dir: source, out: stage, tmpdir: path.join(stage, 'packager-temp'), name: info.productName, platform, arch,
      electronVersion: info.devDependencies.electron,
      appBundleId: 'local.ytdownloader.desktop', appVersion: info.version,
      asar: true, prune: false, extraResource: core,
      ...(platform === 'darwin' ? {
        icon: path.join(desktop, 'assets', 'icon.icns'),
        extendInfo: {
          NSDownloadsFolderUsageDescription: '保存下载的视频，并复用 Downloads 中的视频。',
        },
      } : platform === 'win32' ? {
        icon: path.join(desktop, 'assets', 'icon.ico'),
        win32metadata: {CompanyName: info.productName},
      } : {}),
    });
    if (platform === 'darwin' && process.platform === 'darwin') {
      const app = path.join(bundle, info.productName + '.app');
      const sign = spawnSync('codesign', ['--force', '--deep', '--sign', '-', app], {stdio: 'inherit'});
      if (sign.status !== 0) throw sign.error || new Error('macOS bundle signing failed');
    }
    if (platform === 'linux') {
      await cp(path.join(desktop, 'assets', 'icon.png'), path.join(bundle, 'icon.png'));
      await cp(path.join(desktop, 'install-desktop.sh'), path.join(bundle, 'install-desktop.sh'));
    }
    const name = (platform === 'win32' ? 'windows' : platform) + '-' + (arch === 'x64' ? 'amd64' : arch);
    await mkdir(dir, {recursive: true});
    // Copy into a staging directory on the same filesystem before replacing the previous build.
    const pending = await mkdtemp(path.join(dir, '.desktop-'));
    try {
      const output = path.join(pending, 'app');
      const archive = path.join(pending, 'app.zip');
      await cp(bundle, output, {recursive: true, verbatimSymlinks: true});
      go(['run', './scripts/zip', output, archive], process.env);
      await rm(path.join(dir, name), {recursive: true, force: true});
      await rename(output, path.join(dir, name));
      await rename(archive, path.join(dir, 'yt-downloader-' + name + '.zip'));
      console.log(path.relative(root, path.join(dir, 'yt-downloader-' + name + '.zip')));
    } finally {
      await rm(pending, {recursive: true, force: true});
    }
  } finally {
    await rm(stage, {recursive: true, force: true});
  }
}

if (dev) {
  const location = path.join(desktop, '.dev');
  await mkdir(location, {recursive: true});
  buildCore(process.platform, process.arch, location);
  const {default: electron} = await import('electron');
  const app = spawn(electron, [desktop, ...process.argv.slice(3)], {stdio: 'inherit'});
  app.on('error', error => { console.error(error); process.exitCode = 1; });
  app.on('close', code => { process.exitCode = code ?? 1; });
} else {
  let next = 0;
  const lane = async () => {
    while (next < targets.length) await build(...targets[next++]);
  };
  const results = await Promise.allSettled(Array.from({length: Math.min(targets.length, 2)}, lane));
  const failures = results.filter(result => result.status === 'rejected');
  if (failures.length) throw new AggregateError(failures.map(result => result.reason), 'Desktop build failed');
}
