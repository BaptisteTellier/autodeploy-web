function searchBox() {
  return {
    q: '',
    results: [],
    open: false,
    t: null,

    onInput() {
      clearTimeout(this.t);
      const v = this.q.trim();
      if (!v) {
        this.results = [];
        this.open = false;
        return;
      }
      this.t = setTimeout(() => {
        fetch('/search?q=' + encodeURIComponent(v))
          .then(r => r.json())
          .then(data => {
            this.results = data || [];
            this.open = true;
          })
          .catch(() => {
            this.results = [];
          });
      }, 150);
    },

    close() {
      this.open = false;
    },
  };
}
