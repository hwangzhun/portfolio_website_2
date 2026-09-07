'use strict';

const HAN_CHARACTERS = /\p{Script=Han}/u;

function normalizeUploadFilename(filename) {
  const basename = String(filename || 'upload')
    .replaceAll('\0', '')
    .split(/[\\/]/)
    .pop()
    .normalize('NFC');

  // Multer/Busboy may expose UTF-8 filename bytes as Latin-1 characters.
  // Only accept the recovered value when it is valid UTF-8 and reveals Han
  // characters, so ordinary English and genuinely Latin-1 names stay intact.
  if (HAN_CHARACTERS.test(basename)) return basename;
  const recovered = Buffer.from(basename, 'latin1').toString('utf8').normalize('NFC');
  if (!recovered.includes('\uFFFD') && HAN_CHARACTERS.test(recovered)) return recovered;
  return basename;
}

module.exports = { normalizeUploadFilename };
