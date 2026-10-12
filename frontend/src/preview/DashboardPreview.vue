<template>
  <div class="som-dashboard">
    <div class="som-preview-label">本地配色预览 · 演示数据 · v0.2.15</div>
    <DashboardView />
  </div>
</template>

<script setup lang="ts">
import DashboardView from '@/views/user/DashboardView.vue'
</script>

<style>
.som-preview-label {
  position: fixed; z-index: 35; bottom: 16px; right: 20px;
  padding: 6px 11px; border: 1px solid var(--som-border); border-radius: 999px;
  background: var(--som-surface); color: var(--som-muted); font-size: 10px;
  box-shadow: 0 2px 10px #00000006; pointer-events: none;
}
</style>
