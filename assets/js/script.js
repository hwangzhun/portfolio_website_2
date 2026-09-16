'use strict';
document.querySelectorAll('[data-video-id]').forEach((link) => {
  link.href = `video.html?id=${encodeURIComponent(link.dataset.videoId)}`;
});
const filters = document.querySelectorAll('[data-filter]');
const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
let filterRun = 0;
const cardAnimations = new Map();
const animateCard = (card, keyframes, options) => {
  cardAnimations.get(card)?.cancel();
  const [from, to] = keyframes;
  Object.assign(card.style, { transition: 'none', ...from });
  card.getBoundingClientRect();

  let timer;
  let frame;
  let resolveAnimation;
  const finish = (cancelled = false) => {
    window.clearTimeout(timer);
    cancelAnimationFrame(frame);
    if (cardAnimations.get(card)?.cancel === cancel) cardAnimations.delete(card);
    if (cancelled || !options.fill) {
      card.style.removeProperty('transition');
      card.style.removeProperty('opacity');
      card.style.removeProperty('transform');
    }
    resolveAnimation();
  };
  const cancel = () => finish(true);
  const finished = new Promise((resolve) => { resolveAnimation = resolve; });
  cardAnimations.set(card, { cancel });

  frame = requestAnimationFrame(() => {
    if (cardAnimations.get(card)?.cancel !== cancel) return;
    card.style.transitionProperty = 'opacity, transform';
    card.style.transitionDuration = `${options.duration}ms`;
    card.style.transitionDelay = `${options.delay || 0}ms`;
    card.style.transitionTimingFunction = options.easing;
    Object.assign(card.style, to);
    timer = window.setTimeout(() => finish(), options.duration + (options.delay || 0) + 30);
  });
  return finished;
};
filters.forEach((filter) => filter.addEventListener('click', async () => {
  const run = ++filterRun;
  filters.forEach((button) => button.classList.toggle('active', button === filter));
  const works = [...document.querySelectorAll('[data-category]')];
  works.forEach((work) => cardAnimations.get(work)?.cancel());
  const shouldShow = (work) => filter.dataset.filter === 'all' || work.dataset.category === filter.dataset.filter;
  const leaving = works.filter((work) => !work.hidden && !shouldShow(work));

  if (!reduceMotion) {
    await Promise.all(leaving.map((work) => animateCard(work,
      [{ opacity: 1, transform: 'scale(1)' }, { opacity: 0, transform: 'scale(.985)' }],
      { duration: 160, easing: 'cubic-bezier(.4,0,1,1)', fill: 'forwards' }
    )));
  }
  if (run !== filterRun) return;

  const firstPositions = new Map(works.filter((work) => !work.hidden && shouldShow(work))
    .map((work) => [work, work.getBoundingClientRect()]));
  works.forEach((work) => { work.hidden = !shouldShow(work); });
  leaving.forEach((work) => {
    cardAnimations.get(work)?.cancel();
    work.style.removeProperty('transition');
    work.style.removeProperty('opacity');
    work.style.removeProperty('transform');
  });

  if (reduceMotion) return;
  const entering = works.filter((work) => !work.hidden && !firstPositions.has(work));
  const moving = works.filter((work) => !work.hidden && firstPositions.has(work));
  const lastPositions = new Map(moving.map((work) => [work, work.getBoundingClientRect()]));

  moving.forEach((work) => {
    const first = firstPositions.get(work);
    const last = lastPositions.get(work);
    const x = first.left - last.left;
    const y = first.top - last.top;
    if (x || y) animateCard(work,
      [{ transform: `translate(${x}px, ${y}px)` }, { transform: 'translate(0, 0)' }],
      { duration: 320, easing: 'cubic-bezier(.2,0,0,1)' }
    );
  });
  entering.forEach((work, index) => animateCard(work,
    [{ opacity: 0, transform: 'translateY(12px)' }, { opacity: 1, transform: 'translateY(0)' }],
    { duration: 280, delay: Math.min(index * 35, 105), easing: 'cubic-bezier(.2,0,0,1)' }
  ));
}));

const revealTargets = document.querySelectorAll('.hero,.intro,.works,.experience,footer');
if (!reduceMotion && 'IntersectionObserver' in window) {
  const revealObserver = new IntersectionObserver((entries, observer) => entries.forEach((entry) => {
    if (!entry.isIntersecting) return;
    entry.target.classList.add('is-revealed');
    observer.unobserve(entry.target);
  }), { threshold: .12 });
  revealTargets.forEach((target) => { target.classList.add('reveal'); revealObserver.observe(target); });
} else revealTargets.forEach((target) => target.classList.add('is-revealed'));
