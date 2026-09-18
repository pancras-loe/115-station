<script setup lang="ts">
import { useDialog, useLoadingBar, useMessage, useNotification } from 'naive-ui'
import { registerFeedback } from '@/composables/useFeedback'

// Naive 的 useMessage 等只能在 provider 内部的组件里调用，而 store / api 层
// 也需要弹提示。这个空组件把实例捞出来注册到模块单例，供非组件代码使用。
registerFeedback({
  message: useMessage(),
  dialog: useDialog(),
  notification: useNotification(),
  loadingBar: useLoadingBar(),
})
</script>

<template><span style="display: none" /></template>
