const revealItems = document.querySelectorAll('.reveal');

revealItems.forEach((item, index) => {
    item.style.animationDelay = `${Math.min(index * 0.04, 0.28)}s`;
});

document.querySelectorAll('[data-loading-form]').forEach((form) => {
    form.addEventListener('submit', () => {
        const button = form.querySelector('button[type="submit"]');
        if (!button) return;
        button.dataset.oldText = button.textContent;
        button.textContent = 'Сохраняю...';
        button.disabled = true;
    });
});

document.querySelectorAll('[data-confirm]').forEach((button) => {
    button.addEventListener('click', (event) => {
        const message = button.getAttribute('data-confirm') || 'Продолжить?';
        if (!window.confirm(message)) {
            event.preventDefault();
        }
    });
});
