<!--
  MetricsCardSkeleton — 指标卡的骨架版式。

  按 ServerMetricsCard 可达态的版式逐行排(系统信息 / CPU 负载 / CPU 使用率 / 内存 / 磁盘):
  每行先是标签行,再是值列;骨架块高度对齐真卡的文字行(0.86rem × 1.6 ≈ 20px)与进度条(6px),
  行距也与真卡一致(行内 6px、值列 5px、行间 14px)。
  占位卡因此与真卡同版式同高度:数据一到原地换成真卡,网格不塌也不跳。
  只负责正文;标题行与内边距由外层卡片给(.metrics-card--placeholder / .skeleton-card)。
-->
<script setup lang="ts">
import SkeletonBlock from '../ui/SkeletonBlock.vue'
</script>

<template>
  <div class="mcs" aria-hidden="true">
    <!-- 系统信息:标签 + 发行版一行 + 内核/架构/主机名/运行时长(卡窄时折成两行),下面一条分隔线 -->
    <div class="mcs__row mcs__row--sys">
      <SkeletonBlock :height="20" width="22%" />
      <div class="mcs__values">
        <SkeletonBlock :height="21" width="56%" />
        <SkeletonBlock :height="20" width="92%" />
        <SkeletonBlock :height="20" width="74%" />
      </div>
    </div>
    <!-- CPU 负载:标签 + 负载值 + 负载率 -->
    <div class="mcs__row">
      <SkeletonBlock :height="20" width="26%" />
      <div class="mcs__values">
        <SkeletonBlock :height="21" width="34%" />
        <SkeletonBlock :height="20" width="26%" />
      </div>
    </div>
    <!-- CPU 使用率:标签 + 进度条 + 一行百分比 -->
    <div class="mcs__row">
      <SkeletonBlock :height="20" width="34%" />
      <div class="mcs__values">
        <SkeletonBlock :height="6" width="100%" />
        <SkeletonBlock :height="20" width="44%" />
      </div>
    </div>
    <!-- 内存:标签 + 进度条 + used/total + 含缓存那行 -->
    <div class="mcs__row">
      <SkeletonBlock :height="20" width="22%" />
      <div class="mcs__values">
        <SkeletonBlock :height="6" width="100%" />
        <SkeletonBlock :height="20" width="56%" />
        <SkeletonBlock :height="20" width="62%" />
      </div>
    </div>
    <!-- 交换分区:标签 + 进度条 + 一行字节数 -->
    <div class="mcs__row">
      <SkeletonBlock :height="20" width="20%" />
      <div class="mcs__values">
        <SkeletonBlock :height="6" width="100%" />
        <SkeletonBlock :height="20" width="46%" />
      </div>
    </div>
    <!-- 磁盘:标签 + 进度条 + 一行字节数 -->
    <div class="mcs__row">
      <SkeletonBlock :height="20" width="30%" />
      <div class="mcs__values">
        <SkeletonBlock :height="6" width="100%" />
        <SkeletonBlock :height="20" width="50%" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.mcs {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.mcs__row {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.mcs__values {
  display: flex;
  flex-direction: column;
  gap: 5px;
}
/* 与真卡的系统信息行同款:标签与值挨得更紧 + 下分隔线。 */
.mcs__row--sys {
  gap: 3px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--color-line);
}
</style>
