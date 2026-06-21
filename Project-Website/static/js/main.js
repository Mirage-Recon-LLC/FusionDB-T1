document.addEventListener('DOMContentLoaded', function () {

  // Mobile navigation toggle
  var toggle = document.querySelector('.nav-toggle');
  var navLinks = document.querySelector('.nav-links');
  if (toggle && navLinks) {
    toggle.addEventListener('click', function () {
      navLinks.classList.toggle('open');
    });
    // Close nav on link click
    navLinks.querySelectorAll('a').forEach(function (link) {
      link.addEventListener('click', function () {
        navLinks.classList.remove('open');
      });
    });
  }

  // Slideshow
  var slides = document.querySelectorAll('.slide');
  var dots = document.querySelectorAll('.slide-dot');
  var current = 0;
  var slideTimer = null;

  function showSlide(n) {
    if (!slides.length) return;
    slides[current].classList.remove('active');
    if (dots[current]) dots[current].classList.remove('active');
    current = ((n % slides.length) + slides.length) % slides.length;
    slides[current].classList.add('active');
    if (dots[current]) dots[current].classList.add('active');
  }

  if (slides.length > 1) {
    dots.forEach(function (dot, i) {
      dot.addEventListener('click', function () {
        clearInterval(slideTimer);
        showSlide(i);
        slideTimer = setInterval(function () { showSlide(current + 1); }, 4500);
      });
    });
    slideTimer = setInterval(function () { showSlide(current + 1); }, 4500);
  }

  // Smooth scroll for anchor links
  document.querySelectorAll('a[href^="#"]').forEach(function (anchor) {
    anchor.addEventListener('click', function (e) {
      var target = document.querySelector(this.getAttribute('href'));
      if (target) {
        e.preventDefault();
        target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    });
  });

  // Animate cards on scroll (Intersection Observer)
  var cards = document.querySelectorAll('.card, .dl-card, .doc-link');
  if ('IntersectionObserver' in window && cards.length) {
    cards.forEach(function (el) {
      el.style.opacity = '0';
      el.style.transform = 'translateY(20px)';
      el.style.transition = 'opacity 0.5s ease, transform 0.5s ease';
    });

    var observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (entry.isIntersecting) {
          entry.target.style.opacity = '1';
          entry.target.style.transform = 'translateY(0)';
          observer.unobserve(entry.target);
        }
      });
    }, { threshold: 0.1 });

    cards.forEach(function (el) { observer.observe(el); });
  }
});
