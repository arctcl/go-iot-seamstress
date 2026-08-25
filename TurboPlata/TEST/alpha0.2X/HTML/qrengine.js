// TurboPlata — QR Code Engine
// Чистый JavaScript, генерирует QR-код без зависимостей

(function() {
    'use strict';

    // Полиномы Галуа для QR
    const GF256_EXP = new Array(256);
    const GF256_LOG = new Array(256);
    (function() {
        let v = 1;
        for (let i = 0; i < 256; i++) {
            GF256_EXP[i] = v;
            GF256_LOG[v] = i;
            v = v * 2;
            if (v >= 256) v = v ^ 0x11D;
        }
    })();

    function gfMul(a, b) {
        if (a === 0 || b === 0) return 0;
        return GF256_EXP[(GF256_LOG[a] + GF256_LOG[b]) % 255];
    }

    function gfPolyMul(poly, factor) {
        const result = new Array(poly.length);
        for (let i = 0; i < poly.length; i++) {
            result[i] = gfMul(poly[i], factor);
        }
        return result;
    }

    function gfPolyAdd(a, b) {
        const len = Math.max(a.length, b.length);
        const result = new Array(len).fill(0);
        for (let i = 0; i < a.length; i++) result[i + (len - a.length)] = a[i];
        for (let i = 0; i < b.length; i++) result[i + (len - b.length)] ^= b[i];
        return result;
    }

    // Нахождение генераторного полинома для заданного количества байтов коррекции
    function genPoly(degree) {
        let poly = [1];
        for (let i = 0; i < degree; i++) {
            poly = gfPolyMul(poly, GF256_EXP[i]);
            poly = gfPolyAdd(poly, [1]);
        }
        return poly;
    }

    // Версии QR: [version, totalCodewords, dataCodewords, blocks]
    // Берём только маленькие версии для нашего текста
    const QR_VERSIONS = [
        [1, 26, 19, 1],   // 21x21
        [2, 44, 34, 1],   // 25x25
        [3, 70, 55, 1],   // 29x29
        [4, 100, 80, 1],  // 33x33
        [5, 134, 108, 1], // 37x37
        [6, 172, 136, 2], // 41x41
        [7, 210, 156, 2], // 45x45
        [8, 250, 194, 2], // 49x49
    ];

    // Маски
    const MASKS = [
        (r, c) => (r + c) % 2 === 0,
        (r, c) => r % 2 === 0,
        (r, c) => c % 3 === 0,
        (r, c) => (r + c) % 3 === 0,
        (r, c) => (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0,
        (r, c) => (r * c) % 2 + (r * c) % 3 === 0,
        (r, c) => ((r * c) % 2 + (r * c) % 3) % 2 === 0,
        (r, c) => ((r * c) % 3 + (r + c) % 2) % 2 === 0,
    ];

    function getVersion(dataLen) {
        for (const [ver, total, data, blocks] of QR_VERSIONS) {
            if (data >= dataLen + 3) return { ver, total, data, blocks };
        }
        return QR_VERSIONS[QR_VERSIONS.length - 1];
    }

    // Кодирование alphanumeric
    const ALPHANUM = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:';

    function encodeAlphanum(text) {
        text = text.toUpperCase();
        const bits = [];
        // Режим: alphanumeric (0010)
        bits.push(0, 0, 1, 0);
        // Длина
        const lenBits = text.length <= 9 ? 9 : 11;
        const lenBin = text.length.toString(2).padStart(lenBits, '0');
        for (const b of lenBin) bits.push(parseInt(b));

        // Данные
        for (let i = 0; i < text.length; i += 2) {
            if (i + 1 < text.length) {
                const v = ALPHANUM.indexOf(text[i]) * 45 + ALPHANUM.indexOf(text[i + 1]);
                const b = v.toString(2).padStart(11, '0');
                for (const c of b) bits.push(parseInt(c));
            } else {
                const v = ALPHANUM.indexOf(text[i]);
                const b = v.toString(2).padStart(6, '0');
                for (const c of b) bits.push(parseInt(c));
            }
        }

        // Терминатор
        for (let i = 0; i < 4 && bits.length % 8 !== 0; i++) bits.push(0);

        // Добивка до 8
        while (bits.length % 8 !== 0) bits.push(0);

        return bits;
    }

    // Преобразуем биты в байты
    function bitsToBytes(bits) {
        const bytes = [];
        for (let i = 0; i < bits.length; i += 8) {
            let b = 0;
            for (let j = 0; j < 8; j++) {
                b = (b << 1) | (bits[i + j] || 0);
            }
            bytes.push(b);
        }
        return bytes;
    }

    // Байты в биты (LSB first)
    function bytesToBits(bytes) {
        const bits = [];
        for (const b of bytes) {
            for (let i = 7; i >= 0; i--) {
                bits.push((b >> i) & 1);
            }
        }
        return bits;
    }

    // Создаём QR-матрицу
    function generateQR(text) {
        const vInfo = getVersion(text.length + 3);
        const size = vInfo.ver * 4 + 17;
        const matrix = new Array(size).fill(null).map(() => new Array(size).fill(0));

        // Размещаем Finder patterns
        function setFinder(row, col) {
            for (let r = -1; r <= 7; r++) {
                for (let c = -1; c <= 7; c++) {
                    const nr = row + r, nc = col + c;
                    if (nr < 0 || nr >= size || nc < 0 || nc >= size) continue;
                    const isBorder = r === -1 || r === 7 || c === -1 || c === 7;
                    const isOuter = (r >= 0 && r <= 6 && c >= 0 && c <= 6) && (r === 0 || r === 6 || c === 0 || c === 6);
                    const isCenter = r >= 2 && r <= 4 && c >= 2 && c <= 4;
                    if (isBorder || isOuter || isCenter) matrix[nr][nc] = 1;
                }
            }
        }

        // Separators
        function setSep(row, col, dr, dc) {
            for (let i = 0; i < 8; i++) {
                const nr = row + dr * i, nc = col + dc * i;
                if (nr >= 0 && nr < size && nc >= 0 && nc < size) matrix[nr][nc] = 0;
            }
        }

        setFinder(0, 0);
        setFinder(0, size - 7);
        setFinder(size - 7, 0);
        setSep(0, 7, 0, 1);
        setSep(7, 0, 1, 0);
        setSep(0, size - 8, 0, -1);
        setSep(7, size - 7, 1, 0);
        setSep(size - 8, 0, 0, 1);
        setSep(size - 7, 7, -1, 0);

        // Timing patterns
        for (let i = 8; i < size - 8; i++) {
            matrix[6][i] = matrix[6][i] === 0 ? 1 : matrix[6][i];
            matrix[i][6] = matrix[i][6] === 0 ? 1 : matrix[i][6];
        }

        // Кодируем данные
        const dataBits = encodeAlphanum(text);
        let dataBytes = bitsToBytes(dataBits);

        // Добивка до dataCodewords
        while (dataBytes.length < vInfo.data) {
            dataBytes.push(dataBytes.length % 2 === 0 ? 0xEC : 0x11);
        }

        // Коррекция ошибок (RS)
        const eccCount = vInfo.total - vInfo.data;
        const gp = genPoly(eccCount);

        // Разделяем на блоки
        const blocks = vInfo.blocks || 1;
        const blockSize = Math.floor(vInfo.data / blocks);
        const allBlocks = [];
        for (let b = 0; b < blocks; b++) {
            const start = b * blockSize;
            const end = start + blockSize;
            const block = dataBytes.slice(start, end);

            // RS encoding для блока
            const msg = [...block];
            for (let i = 0; i < eccCount; i++) msg.push(0);
            for (let i = 0; i < block.length; i++) {
                if (msg[i] !== 0) {
                    const factor = GF256_LOG[msg[i]];
                    for (let j = 0; j < msg.length; j++) {
                        msg[j] ^= gfMul(gp[j], GF256_EXP[(factor + GF256_LOG[gp[j]]) % 255]);
                    }
                }
            }
            const eccPart = msg.slice(block.length);
            allBlocks.push({ data: block, ecc: eccPart });
        }

        // Интерливинг данных
        const interleaved = [];
        for (let i = 0; i < blockSize; i++) {
            for (const b of allBlocks) {
                if (i < b.data.length) interleaved.push(b.data[i]);
            }
        }
        for (let i = 0; i < eccCount; i++) {
            for (const b of allBlocks) {
                if (i < b.ecc.length) interleaved.push(b.ecc[i]);
            }
        }

        const allBits = bytesToBits(interleaved);

        // Размещаем данные в матрицу
        let bitIdx = 0;
        let dir = -1; // снизу вверх
        for (let col = size - 1; col > 0; col -= 2) {
            if (col === 6) col--;
            for (let row = dir === -1 ? size - 1 : 0; row >= 0 && row < size; row += dir) {
                for (let dx = 0; dx < 2; dx++) {
                    const c = col - dx;
                    if (matrix[row][c] === 0 && bitIdx < allBits.length) {
                        matrix[row][c] = allBits[bitIdx++];
                    }
                }
            }
            dir = -dir;
        }

        // Применяем маску (mask 0 для простоты)
        const bestMask = 0;
        for (let r = 0; r < size; r++) {
            for (let c = 0; c < size; c++) {
                if (matrix[r][c] === 0 || matrix[r][c] === 1) continue;
                if (MASKS[bestMask](r, c)) matrix[r][c] = matrix[r][c] === 2 ? 1 : 0;
            }
        }

        // Format info
        const formatBits = [1, 0, 1, 0, 0, 0, 1, 1, 1, 0, 1, 0, 0, 1, 0]; // mask 0 + EC L
        const fmtPositions = [
            [0,8],[1,8],[2,8],[3,8],[4,8],[5,8],[7,8],[8,8],[8,7],[8,5],[8,4],[8,3],[8,2],[8,1],[8,0],
            [8, size-1],[8, size-2],[8, size-3],[8, size-4],[8, size-5],[8, size-6],[8, size-7],
            [size-8,8],[size-7,8],[size-6,8],[size-5,8],[size-4,8],[size-3,8],[size-2,8],[size-1,8],
        ];
        for (let i = 0; i < 15; i++) {
            const [r, c] = fmtPositions[i];
            if (r < size && c < size && r >= 0 && c >= 0) matrix[r][c] = formatBits[i];
        }

        return matrix;
    }

    /**
     * Генерирует SVG QR-кода
     * @param {string} text — данные для кодирования
     * @param {object} opts — { size, color, bgColor }
     * @returns {string} — SVG разметка
     */
    function renderQR(text, opts) {
        opts = opts || {};
        const size = opts.size || 200;
        const color = opts.color || '#000000';
        const bgColor = opts.bgColor || '#ffffff';

        // Ограничиваем длину
        const maxLen = 80;
        const clean = text.replace(/\*/g, '').slice(0, maxLen);

        const matrix = generateQR(clean);
        const moduleSize = size / matrix.length;
        const pad = 2;

        let svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" width="${size}" height="${size}">`;
        svg += `<rect x="0" y="0" width="${size}" height="${size}" fill="${bgColor}"/>`;

        for (let r = 0; r < matrix.length; r++) {
            for (let c = 0; c < matrix[r].length; c++) {
                if (matrix[r][c] === 1) {
                    const x = pad + c * moduleSize;
                    const y = pad + r * moduleSize;
                    svg += `<rect x="${x}" y="${y}" width="${moduleSize}" height="${moduleSize}" fill="${color}"/>`;
                }
            }
        }

        svg += '</svg>';
        return svg;
    }

    // Экспорт
    window.TurboQR = {
        svg: renderQR
    };
})();