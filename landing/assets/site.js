/* Theme toggle + copy buttons. Everything works without JS. */
(function () {
  var root = document.documentElement;
  var btn = document.getElementById("theme-toggle");
  if (btn) {
    btn.hidden = false;
    btn.addEventListener("click", function () {
      var dark = root.getAttribute("data-theme") === "dark" ||
        (!root.getAttribute("data-theme") && matchMedia("(prefers-color-scheme: dark)").matches);
      var next = dark ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try { localStorage.setItem("eddy-theme", next); } catch (e) {}
    });
  }
  document.querySelectorAll(".copy").forEach(function (b) {
    b.hidden = false;
    b.addEventListener("click", function () {
      var pre = b.parentNode.querySelector("pre");
      var text = pre.innerText.replace(/^\$ /gm, "");
      var done = function () { b.textContent = "Copied"; setTimeout(function () { b.textContent = "Copy"; }, 1600); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () {});
      }
    });
  });
})();
