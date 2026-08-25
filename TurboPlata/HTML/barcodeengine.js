// TurboPlata — Code39 Barcode Engine
// Чистый JavaScript, рендерит Code39 на canvas без шрифтов

(function() {
    'use strict';

    // Таблица символов Code39: ключ → 9-битный шаблон (bar/spc/bar/spc/bar/spc/bar/spc/bar)
    // 1 = широкий элемент, 0 = узкий элемент
    // Ровно 3 единицы из 9
    const CODE39 = {
        '0': '000110100', '1': '100100001', '2': '001100001',
        '3': '101100000', '4': '000110001', '5': '100110000',
        '6': '001110000', '7': '000100101', '8': '100100100',
        '9': '001100100', 'A': '100001001', 'B': '001001001',
        'C': '101001000', 'D': '000011001', 'E': '100011000',
        'F': '001011000', 'G': '000001101', 'H': '100001100',
        'I': '001001100', 'J': '000011100', 'K': '100000011',
        'L': '001000011', 'M': '101000010', 'N': '000010011',
        'O': '100010010', 'P': '001010010', 'Q': '000000111',
        'R': '100000110', 'S': '001000110', 'T': '000010110',
        'U': '110000001', 'V': '011000001', 'W': '111000000',
        'X': '010010001', 'Y': '110010000', 'Z': '011010000',
        '-': '010000101', '.': '110000100', ' ': '011000100',
        '$': '010101000', '/': '010100010', '+': '010001010',
        '%': '000101010', '*': '010010100' // start/stop
    };

    /**
     * Сгенерировать SVG строку штрихкода Code39
     * @param {string} text — данные для кодирования (без * — добавится автоматически)
     * @param {object} opts — { height, narrow, wide, color, bgColor }
     * @returns {string} — SVG разметка
     */
    function renderBarcodeSVG(text, opts) {
        opts = opts || {};
        const height = opts.height || 100;
        const narrow = opts.narrow || 1;   // ширина узкого элемента (px)
        const wide = opts.wide || 2;          // ширина широкого элемента (px)
        const color = opts.color || '#000000';
        const bgColor = opts.bgColor || '#ffffff';

        // Оборачиваем в * (Code39 требует * для старта/стопа)
        const clean = text.toUpperCase().replace(/\*/g, '');
        const encoded = '*' + clean + '*';

        // Строим последовательность полос
        let x = 0;
        let bars = [];

        for (let c of encoded) {
            const pattern = CODE39[c];
            if (!pattern) continue; // символ не из Code39 — пропускаем

            // pattern: 9 символов — чередуются bar(1,3,5,7,9) и space(2,4,6,8)
            for (let i = 0; i < 9; i++) {
                const isWide = pattern[i] === '1';
                const width = isWide ? wide : narrow;
                // Чётные индексы (0,2,4,6,8) — штрихи, нечётные (1,3,5,7) — пробелы
                if (i % 2 === 0) {
                    // Штрих
                    bars.push({ x: x, w: width, isBar: true });
                } else {
                    // Пробел — рендерим как пустое пространство
                    bars.push({ x: x, w: width, isBar: false });
                }
                x += width;
            }
            // Межсимвольный пробел (узкий)
            x += narrow;
        }

        // Собираем SVG
        const totalWidth = x;
        let svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${totalWidth} ${height}" width="${totalWidth}" height="${height}">`;
        svg += `<rect x="0" y="0" width="${totalWidth}" height="${height}" fill="${bgColor}"/>`;

        for (let bar of bars) {
            if (bar.isBar) {
                svg += `<rect x="${bar.x}" y="0" width="${bar.w}" height="${height}" fill="${color}"/>`;
            }
        }

        svg += '</svg>';
        return svg;
    }

    /**
     * Рендерит Code39 на canvas элемент
     * @param {HTMLCanvasElement|string} canvas — элемент или его ID
     * @param {string} text — данные для кодирования
     * @param {object} opts — { height, narrow, wide, color, bgColor }
     */
    function renderBarcodeCanvas(canvas, text, opts) {
        if (typeof canvas === 'string') {
            canvas = document.getElementById(canvas);
        }
        if (!canvas) return;

        opts = opts || {};
        const height = opts.height || 50;
        const narrow = opts.narrow || 1.5;
        const wide = opts.wide || 3;
        const color = opts.color || '#000000';
        const bgColor = opts.bgColor || '#ffffff';

        const clean = text.toUpperCase().replace(/\*/g, '');
        const encoded = '*' + clean + '*';

        // Ширина пиксельная, поэтому округляем
        let x = 0;
        let bars = [];

        for (let c of encoded) {
            const pattern = CODE39[c];
            if (!pattern) continue;
            for (let i = 0; i < 9; i++) {
                const isWide = pattern[i] === '1';
                const w = isWide ? wide : narrow;
                if (i % 2 === 0) {
                    bars.push({ x: Math.round(x), w: Math.round(w) });
                }
                x += w;
            }
            x += narrow; // межсимвольный пробел
        }

        const totalWidth = Math.ceil(x);
        const dpr = window.devicePixelRatio || 1;
        canvas.width = totalWidth * dpr;
        canvas.height = height * dpr;
        canvas.style.width = totalWidth + 'px';
        canvas.style.height = height + 'px';

        const ctx = canvas.getContext('2d');
        ctx.scale(dpr, dpr);

        // Фон
        ctx.fillStyle = bgColor;
        ctx.fillRect(0, 0, totalWidth, height);

        // Штрихи
        ctx.fillStyle = color;
        for (let bar of bars) {
            ctx.fillRect(bar.x, 0, bar.w || wide, height);
        }
    }

    /**
     * Быстрый рендер в HTML-строку с use-тегом (inline SVG)
     * @param {string} text — данные для кодирования
     * @param {object} opts — опции
     * @returns {string} — HTML с инлайн SVG
     */
    function barcodeSVGHTML(text, opts) {
        return renderBarcodeSVG(text, opts);
    }

    // Экспорт
    window.TurboBarcode = {
        svg: renderBarcodeSVG,
        canvas: renderBarcodeCanvas,
        html: barcodeSVGHTML
    };
})();