function getRandomInt(min, max) {
    return Math.floor(Math.random() * (max - min + 1)) + min;
}

let screenX = getRandomInt(800, 1200);
let screenY = getRandomInt(400, 700);

Object.defineProperty(MouseEvent.prototype, 'screenX', {
    get: function () {
        return screenX + this.clientX;
    }
});

Object.defineProperty(MouseEvent.prototype, 'screenY', {
    get: function () {
        return screenY + this.clientY;
    }
});

// Capture the host page's Turnstile callback so an externally minted token can
// be delivered into the site's own widget state. x.ai's React form reads the
// token from this callback (not from the cf-turnstile-response input), so a
// token written only to the DOM is ignored. window.__cfSolve(token) replays the
// token through every captured callback.
(function () {
    window.__cfCallbacks = window.__cfCallbacks || [];
    window.__cfSolve = function (token) {
        let n = 0;
        for (const cb of window.__cfCallbacks) {
            try { cb(token); n++; } catch (e) { }
        }
        return n;
    };

    // Wrap turnstile.render (without redefining window.turnstile, which breaks
    // x.ai's page) to record the site's callback. Poll because api.js and the
    // React render happen after this content script runs.
    function wrapRender(ts) {
        try {
            if (!ts || typeof ts.render !== 'function' || ts.__cfWrapped) return;
            const real = ts.render.bind(ts);
            ts.render = function (el, opts) {
                try {
                    if (opts && typeof opts.callback === 'function') {
                        window.__cfCallbacks.push(opts.callback);
                    }
                } catch (e) { }
                return real(el, opts);
            };
            try {
                Object.defineProperty(ts, '__cfWrapped', { value: true, enumerable: false });
            } catch (e) { ts.__cfWrapped = true; }
        } catch (e) { }
    }

    let tries = 0;
    const iv = setInterval(function () {
        try { if (window.turnstile) wrapRender(window.turnstile); } catch (e) { }
        if (++tries > 3000) clearInterval(iv);
    }, 100);
})();
