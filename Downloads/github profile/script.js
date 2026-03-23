// Typing animation
    const phrases = [
      "Swinging Through Servers with Go...",
      "Friendly Neighborhood Backend Dev...",
      "Bitten by Logic. Turned Into a Dev...",
      "Building APIs. Not dreams. Systems."
    ];
    let pi = 0, ci = 0, del = false;
    const el = document.getElementById('typed');
    function type() {
      const s = phrases[pi];
      if (!del) {
        el.textContent = s.slice(0, ++ci);
        if (ci === s.length) { del = true; setTimeout(type, 1600); return; }
      } else {
        el.textContent = s.slice(0, --ci);
        if (ci === 0) { del = false; pi = (pi + 1) % phrases.length; }
      }
      setTimeout(type, del ? 38 : 65);
    }
    type();

    // Animate bars
    setTimeout(() => {
      [['l0', 40], ['l1', 30], ['l2', 20], ['l3', 10]].forEach(([id, w]) => {
        const e = document.getElementById(id);
        if (e) e.style.width = w + '%';
      });
    }, 400);
