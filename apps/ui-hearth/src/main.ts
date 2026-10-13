import { createApp } from 'vue';
import '@hearth-ui/vue/styles.css';
import App from './App.vue';
import { router } from './router';

createApp(App).use(router).mount('#app');
